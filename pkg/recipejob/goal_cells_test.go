package recipejob_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// No runtime embedding: discovery cannot acquire lifecycle authority by promotion.
type cellGoalReader struct {
	list  func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)
	goals func(context.Context, jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error)
}

func (r cellGoalReader) ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return r.list(ctx, req)
}
func (r cellGoalReader) ListGoals(ctx context.Context, req jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error) {
	return r.goals(ctx, req)
}
func (r cellGoalReader) ListDependencyWaits(context.Context, jobdb.ListDependencyWaitsRequest) (jobdb.ListDependencyWaitsResponse, error) {
	panic("unexpected wait read")
}

var _ recipejob.CellGoalDiscovery = cellGoalReader{}

func TestC03CellDiscoveryPreservesFiltersAndOpaquePages(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	before := now.Add(time.Hour)
	req := jobdb.ListGoalsRequest{TenantId: "tenant", GoalIDs: []string{"G"}, States: []jobdb.GoalState{jobdb.GoalSucceeded}, CreatedAfter: &now, CreatedBefore: &before, UpdatedAfter: &now, UpdatedBefore: &before, PageSize: 3, PageToken: " goal token/+= ", Candidate: &jobdb.GoalCandidateFilter{JobIDs: []string{"B", "absent"}, JobTypes: []string{"external"}, MetadataEquals: []jobdb.MetadataPredicate{{Path: []string{"app", "key"}, Values: []any{"value"}}}}}
	original := *req.Candidate
	want := jobdb.ListGoalsResponse{Goals: []jobdb.GoalSummary{{GoalInfo: jobdb.GoalInfo{GoalKey: jobdb.GoalKey{TenantId: "tenant", GoalID: "G"}, SuccessfulJobID: "B"}, CandidateCount: 17, TerminalCandidateCount: 12, NonTerminalCandidateCount: 3, UnavailableCandidateCount: 2}}, NextPageToken: " next goal/+= "}
	tokens := []string{"", " empty/+= ", " final/+= "}
	calls := 0
	r := cellGoalReader{
		list: func(_ context.Context, got jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
			require.Equal(t, []string{"tenant"}, got.TenantIds)
			require.Equal(t, []string{"app"}, got.CellNames)
			require.Equal(t, []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, got.Stores)
			require.True(t, got.IncludeHidden)
			require.Equal(t, jobdb.MaxListJobsPageSize, got.PageSize)
			require.Equal(t, tokens[calls], got.PageToken)
			calls++
			switch calls {
			case 1:
				return jobdb.ListJobsResponse{NextPageToken: tokens[1]}, nil
			case 2:
				return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{{JobKey: jobdb.JobKey{JobId: "B"}, GoalID: "H"}, {JobKey: jobdb.JobKey{JobId: "unrequested"}}}, NextPageToken: tokens[2]}, nil
			default:
				return jobdb.ListJobsResponse{}, nil
			}
		},
		goals: func(_ context.Context, got jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error) {
			expected := req
			filter := original
			filter.JobIDs = []string{"B"}
			expected.Candidate = &filter
			require.Equal(t, expected, got)
			return want, nil
		},
	}
	got, err := recipejob.ListGoalsInCells(ctx, r, req, []string{"app"})
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, 3, calls)
	require.Equal(t, original, *req.Candidate, "caller filter must remain unchanged")
}

func TestC03CellDiscoveryNilEmptyAndErrors(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("backend unavailable")
	for i, cells := range [][]string{nil, {}, {"app"}} {
		for _, mode := range []string{"empty", "lookup error", "discovery error"} {
			t.Run(fmt.Sprintf("%s/cells-%d", mode, i), func(t *testing.T) {
				lists, goals := 0, 0
				r := cellGoalReader{list: func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
					lists++
					require.Equal(t, cells, req.CellNames)
					if mode == "lookup error" {
						return jobdb.ListJobsResponse{NextPageToken: "partial"}, cause
					}
					if mode == "discovery error" {
						return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{{JobKey: jobdb.JobKey{JobId: "B"}}}}, nil
					}
					return jobdb.ListJobsResponse{}, nil
				}, goals: func(context.Context, jobdb.ListGoalsRequest) (jobdb.ListGoalsResponse, error) {
					goals++
					if mode != "empty" {
						return jobdb.ListGoalsResponse{}, cause
					}
					return jobdb.ListGoalsResponse{}, nil
				}}
				_, err := recipejob.ListGoalsInCells(ctx, r, jobdb.ListGoalsRequest{TenantId: "t"}, cells)
				if mode != "empty" {
					require.ErrorIs(t, err, cause)
				} else {
					require.NoError(t, err)
				}
				if cells == nil {
					require.Zero(t, lists)
					require.Equal(t, 1, goals)
				} else {
					require.Equal(t, 1, lists)
					if mode == "discovery error" {
						require.Equal(t, 1, goals)
					} else {
						require.Zero(t, goals)
					}
				}
			})
		}
	}
	// Lister alone suffices for job IDs; empty requested IDs mean unrestricted.
	lister := &completionListOnly{list: func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{{JobKey: jobdb.JobKey{JobId: "B"}}}}, nil
	}}
	ids, err := recipejob.JobIDsInCells(ctx, lister, "t", []string{}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"B"}, ids)
}

func TestC03CellDiscoveryValidatesBeforeEmptyLookup(t *testing.T) {
	now := time.Now()
	before := now.Add(-time.Hour)
	invalid := []jobdb.ListGoalsRequest{
		{}, {TenantId: "  "}, {TenantId: "t", PageSize: -1}, {TenantId: "t", States: []jobdb.GoalState{"invalid"}},
		{TenantId: "t", CreatedAfter: &now, CreatedBefore: &before}, {TenantId: "t", UpdatedAfter: &now, UpdatedBefore: &before},
		{TenantId: "t", Candidate: &jobdb.GoalCandidateFilter{MetadataEquals: []jobdb.MetadataPredicate{{Path: []string{"x"}, Values: []any{nil}}}}},
	}
	for _, req := range invalid {
		for _, cells := range [][]string{nil, {}, {"app"}} {
			_, err := recipejob.ListGoalsInCells(context.Background(), cellGoalReader{}, req, cells)
			require.Error(t, err, "invalid requests must fail before any dispatch")
		}
	}
}

func TestReadOnlyFixtureMethodSets(t *testing.T) {
	for _, v := range []any{completionListOnly{}, &completionListOnly{}, cellGoalReader{}, &cellGoalReader{}} {
		typ := reflect.TypeOf(v)
		for _, name := range []string{"AddGoalCandidate", "AttachJobToGoal", "FailGoal", "SubmitJob", "CancelJob"} {
			_, ok := typ.MethodByName(name)
			require.False(t, ok, "%s promotes %s", typ, name)
		}
	}
}
