package recipejob_test

import (
	"context"
	"errors"

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

func TestExactJobCellGoalDiscoveryNeverDispatches(t *testing.T) {
	for _, cells := range [][]string{nil, {}, {"app"}} {
		_, err := recipejob.ListGoalsInCells(context.Background(), cellGoalReader{}, jobdb.ListGoalsRequest{TenantId: "t"}, cells)
		require.ErrorIs(t, err, errors.ErrUnsupported)
	}
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
