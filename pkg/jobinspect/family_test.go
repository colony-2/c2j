package jobinspect

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type familyRuntime struct {
	jobdb.WorkflowRuntime // any unexpected enrichment panics
	calls                 []jobdb.ListJobsRequest
	page                  jobdb.ListJobsResponse
	err                   error
}

func (r *familyRuntime) ListJobs(_ context.Context, q jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.calls = append(r.calls, q)
	return r.page, r.err
}
func familyRequest(op string) Request {
	return Request{Operation: op, JobID: "job", Limit: 100, Family: &FamilyOptions{Version: 1}}
}
func TestFamilyCompactRead(t *testing.T) {
	for _, op := range []string{"family-node", "family-children"} {
		t.Run(op, func(t *testing.T) {
			archived := time.Now().UTC()
			row := jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, ParentJobID: "runtime-parent", Cell: "other-cell", GoalID: "goal", JobType: "generic", Status: "FUTURE_STATUS", CompletionStatus: "future_outcome", CancelRequested: true, ArchivedAt: &archived, Metadata: json.RawMessage(`{"title":"Test title","parent_job_id":"forged"}`)}
			rt := &familyRuntime{page: jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{row}}}
			authorized := 0
			s := &Service{Runtime: rt, Tenant: "tenant", Parent: "ignored", Authorize: func(_ context.Context, id string) error {
				require.Empty(t, rt.calls)
				require.Equal(t, "job", id)
				authorized++
				return nil
			}}
			q := familyRequest(op)
			if op == "family-children" {
				q.Cursor = "opaque+/="
				rt.page.NextPageToken = "next"
			}
			r, err := s.Inspect(context.Background(), q)
			require.NoError(t, err)
			require.Equal(t, 1, authorized)
			require.Len(t, rt.calls, 1)
			got := rt.calls[0]
			want := jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, IncludeHidden: true, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, PageSize: 100, PageToken: q.Cursor}
			if op == "family-node" {
				want.JobKeys = []jobdb.JobKey{{TenantId: "tenant", JobId: "job"}}
			} else {
				want.ParentJobIDs = []string{"job"}
			}
			require.Equal(t, want, got)
			require.Equal(t, 1, r.Family.Version)
			require.Len(t, r.Family.Nodes, 1)
			n := r.Family.Nodes[0]
			require.Equal(t, "runtime-parent", n.Parent)
			require.Equal(t, "Test title", n.Title)
			require.Equal(t, "generic", n.JobType)
			require.Equal(t, "goal", n.Goal)
			require.Equal(t, "FUTURE_STATUS", n.Status)
			require.Equal(t, "future_outcome", n.CompletionStatus)
			require.True(t, n.CancelRequested)
			require.Equal(t, jobdb.JobStoreArchived, n.Store)
			sentinel := errors.New("authorize denied")
			s.Authorize = func(context.Context, string) error { return sentinel }
			_, err = s.Inspect(context.Background(), q)
			require.ErrorIs(t, err, sentinel)
			require.Len(t, rt.calls, 1)
		})
	}
}
func TestFamilyRejectsSelectorsBeforeReads(t *testing.T) {
	base := familyRequest("family-children")
	changes := []func(*Request){
		func(q *Request) { q.JobID = "" }, func(q *Request) { q.JobID = "self" }, func(q *Request) { q.Family = nil }, func(q *Request) { q.Family = &FamilyOptions{Version: 2} }, func(q *Request) { q.Limit = 0 }, func(q *Request) { q.Limit = 101 }, func(q *Request) { q.Limit = -1 },
		func(q *Request) { q.Task = "task" }, func(q *Request) { q.Attempt = 1 }, func(q *Request) { q.Field = new(string) }, func(q *Request) { q.Parent = "other" }, func(q *Request) { q.Status = []jobdb.JobStatus{} }, func(q *Request) { q.Name = "x" }, func(q *Request) { q.Reference = &jobdb.ArtifactKey{} }, func(q *Request) { q.Workspace = "x" }, func(q *Request) { q.Boundary = "before" }, func(q *Request) { q.Events.IncludeHidden = true }, func(q *Request) { q.Operation = "show" }, func(q *Request) { q.Operation = "family-node"; q.Cursor = "cursor" },
	}
	rt := &familyRuntime{}
	s := &Service{Runtime: rt, Tenant: "tenant", Authorize: func(context.Context, string) error { t.Fatal("unexpected authorization"); return nil }}
	for i, change := range changes {
		q := base
		change(&q)
		_, err := s.Inspect(context.Background(), q)
		require.Error(t, err, "case %d", i)
	}
	require.Empty(t, rt.calls)
}
func TestFamilyExactValidationAndTitles(t *testing.T) {
	valid := jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, Status: "COMPLETED"}
	for _, tc := range []struct {
		name   string
		rows   []jobdb.JobSummary
		cursor string
	}{
		{"absent", nil, ""}, {"duplicate", []jobdb.JobSummary{valid, valid}, ""}, {"continuation", []jobdb.JobSummary{valid}, "next"}, {"foreign", []jobdb.JobSummary{{JobKey: jobdb.JobKey{TenantId: "other", JobId: "job"}}}, ""}, {"wrong-id", []jobdb.JobSummary{{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "wrong"}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{Tenant: "tenant", Runtime: &familyRuntime{page: jobdb.ListJobsResponse{Jobs: tc.rows, NextPageToken: tc.cursor}}}
			_, err := s.Inspect(context.Background(), familyRequest("family-node"))
			require.Error(t, err)
		})
	}
	for _, metadata := range []string{"", `{`, `{"parent_job_id":"forged"}`, `{"title":42}`, `{"title":"partial","bad":`} {
		valid.Metadata = json.RawMessage(metadata)
		s := &Service{Tenant: "tenant", Runtime: &familyRuntime{page: jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{valid}}}}
		r, err := s.Inspect(context.Background(), familyRequest("family-node"))
		require.NoError(t, err)
		require.Empty(t, r.Family.Nodes[0].Title)
		require.Empty(t, r.Family.Nodes[0].Parent)
		require.Equal(t, "COMPLETED", r.Family.Nodes[0].Status)
	}
	s := &Service{Tenant: "tenant", Runtime: &familyRuntime{}}
	r, err := s.Inspect(context.Background(), familyRequest("family-children"))
	require.NoError(t, err)
	require.NotNil(t, r.Family.Nodes)
}
