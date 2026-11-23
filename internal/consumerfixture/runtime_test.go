package consumerfixture_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/consumerfixture"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

// Both constructors receive a retained-only decorator. Snapshot every job/goal,
// exhaust candidate and wait cursors, and compare full records after old PUTs.
func TestC06ConsumerHTTPRouteDoesNotMutate(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	rt := consumerfixture.New(db)
	for _, value := range []any{rt, &rt} {
		_, found := reflect.TypeOf(value).MethodByName("AddGoalCandidate")
		require.False(t, found)
	}
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	submit := func(id string, deps ...jobdb.Dependency) jobdb.JobHandle {
		h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: id, JobType: "candidate", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil), Dependencies: deps}})
		require.NoError(t, err)
		return h
	}
	a, b := submit("a"), submit("b")
	c := submit("consumer", jobdb.Dependency{Target: jobdb.DependencyTarget{GoalID: a.GoalID}, Condition: jobdb.JobPrereqSuccess})
	type state struct {
		Jobs       []jobdb.JobInfo
		Goals      []jobdb.GoalInfo
		Candidates []jobdb.JobSummary
		Waits      []jobdb.DependencyWaitInfo
	}
	snapshot := func() state {
		var s state
		for _, h := range []jobdb.JobHandle{a, b, c} {
			job, err := rt.GetJob(ctx, h.JobKey)
			require.NoError(t, err)
			s.Jobs = append(s.Jobs, job)
			goal, err := rt.GetGoal(ctx, jobdb.GoalKey{TenantId: "tenant", GoalID: h.GoalID})
			require.NoError(t, err)
			s.Goals = append(s.Goals, goal)
			for cursor := ""; ; {
				page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, GoalIDs: []string{goal.GoalKey.GoalID}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, IncludeHidden: true, PageSize: 1, PageToken: cursor})
				require.NoError(t, err)
				s.Candidates = append(s.Candidates, page.Jobs...)
				if page.NextPageToken == "" {
					break
				}
				require.NotEqual(t, cursor, page.NextPageToken)
				cursor = page.NextPageToken
			}
			for cursor := ""; ; {
				page, err := rt.ListDependencyWaits(ctx, jobdb.ListDependencyWaitsRequest{TenantId: "tenant", ConsumerJobID: h.JobKey.JobId, PageSize: 1, PageToken: cursor})
				require.NoError(t, err)
				s.Waits = append(s.Waits, page.Waits...)
				if page.NextPageToken == "" {
					break
				}
				require.NotEqual(t, cursor, page.NextPageToken)
				cursor = page.NextPageToken
			}
		}
		return s
	}
	before := snapshot()
	require.NotEmpty(t, before.Waits)
	configured, err := remote.NewConsumerServerWithOptions(rt, remote.ServerOptions{LeaseTokenSigningKey: []byte(strings.Repeat("k", 32))})
	require.NoError(t, err)
	for _, handler := range []http.Handler{remote.NewConsumerServer(rt), configured} {
		for _, target := range [][2]string{{a.GoalID, b.JobKey.JobId}, {b.GoalID, a.JobKey.JobId}} {
			req := httptest.NewRequest(http.MethodPut, "/v1/tenants/tenant/goals/"+target[0]+"/candidates/"+target[1], nil)
			recorder := httptest.NewRecorder()
			celltest.Handler(handler).ServeHTTP(recorder, req)
			require.Equal(t, http.StatusNotFound, recorder.Code)
			require.Equal(t, before, snapshot())
		}
	}
}
