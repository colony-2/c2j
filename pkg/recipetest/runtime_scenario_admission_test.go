package recipetest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobref"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestScenarioAdmissionTargetValidation(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	for _, tc := range []struct {
		name, recipe string
		route        jobdb.Route
		want         string
	}{
		{"matching target", "actual", jobdb.Route{JobType: "recipe"}, ""},
		{"unsubmitted recipe", "never-submitted", jobdb.Route{JobType: "recipe"}, "recipe mismatch"},
		{"wrong job route", "actual", jobdb.Route{JobType: "never-submitted"}, "route mismatch"},
		{"wrong task route", "actual", jobdb.Route{JobType: "recipe", TaskType: "never-submitted"}, "route mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Case{ID: "admission-target", Type: "integration_case", Runtime: &RuntimeCase{Backend: "sqlite", Controls: &RuntimeControls{Scenario: &Scenario{
				Jobs:           map[string]ScenarioJob{"root": {Root: true}},
				AdmissionHolds: []AdmissionHold{{ID: "selected", Cell: "root", Recipe: tc.recipe, Route: tc.route, Jobs: []string{"root"}}},
				Events:         []ScenarioEvent{{ID: "submitted", Kind: "submission", Job: "root"}},
				Actions:        []ScenarioAction{{ID: "release", AfterAll: []string{"submitted"}, Do: "release", Target: "selected"}},
				Outcomes:       map[string]ScenarioOutcome{"root": {Status: "completed"}},
			}}}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{NodePath: "actual/work"}, Behavior: MockBehavior{Mode: "return"}}}}}
			target := TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: "id: actual\nsequence:\n- id: work\n  op: command_execution\n  inputs: {run: unused}\noutputs: {}\n"}
			r := runScenario(t, c, target, HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()})
			if tc.want == "" {
				require.Equal(t, "passed", r.Status, r.FailureReason)
				require.True(t, r.Runtime.Controls.Scenario.Complete)
				return
			}
			require.Equal(t, "failed", r.Status)
			require.Contains(t, r.FailureReason, tc.want)
			require.False(t, r.Runtime.Controls.Complete)
			require.False(t, r.Runtime.Controls.Scenario.Complete)
			require.Empty(t, r.Runtime.Controls.Scenario.Actions, "a mismatched hold must never count as released")
		})
	}
}

func TestScenarioTargetedAdmissionNeverLeasesHeldReceiver(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, closeRT, err := newCaseRuntime(ctx, t.TempDir(), "admission", &RuntimeCase{Backend: "sqlite"})
	require.NoError(t, err)
	defer closeRT()
	_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "admission", Name: "root", Repo: "example.test/receiver"})
	require.NoError(t, err)
	spec := &Scenario{Jobs: map[string]ScenarioJob{"root": {Root: true}}, AdmissionHolds: []AdmissionHold{{ID: "batch", Cell: "root", Recipe: "receiver", Route: jobdb.Route{JobType: "native-probe"}, Jobs: []string{"receiver"}}}}
	f := &runtimeFixture{c: Case{Runtime: &RuntimeCase{Backend: "sqlite", Controls: &RuntimeControls{Scenario: spec}}}, seededRepos: map[string]string{"root": "example.test/receiver"}}
	f.initControls()
	controlled := scenarioRuntime{rt, f}
	submit := func() jobdb.JobHandle {
		h, e := rt.StartOrAppendJob(ctx, jobdb.StartOrAppendJobRequest{Job: jobdb.StartOrAppendJob{Job: jobdb.SubmitJob{TenantId: "admission", Cell: "root", CellRepo: "example.test/receiver", JobType: "native-probe", Metadata: json.RawMessage(`{"recipe":"receiver"}`), Data: jobdb.NewTaskDataOrPanic(map[string]any{})}, AppendKey: "batch", MaxInputs: 3, Append: json.RawMessage(`{"content":{"id":1}}`)}})
		require.NoError(t, e)
		return h
	}
	one := submit()
	lease, err := controlled.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: one.JobKey, WorkerID: "targeted", Routes: []jobdb.Route{{JobType: "native-probe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.Nil(t, lease)
	two := submit()
	require.Equal(t, one.JobKey, two.JobKey, "a held acquisition must not force a successor")
	until := time.Now().Add(time.Millisecond)
	leases, err := controlled.PollWork(ctx, jobdb.PollWorkRequest{TenantId: "admission", WorkerID: "poll", Routes: []jobdb.Route{{JobType: "native-probe"}}, LongPollUntil: &until})
	require.NoError(t, err)
	require.Empty(t, leases)
	require.ErrorContains(t, f.releaseScenarioHold(ctx, rt, "batch"), "unresolved job")
	ref, err := jobref.FromHandle(one)
	require.NoError(t, err)
	f.scenario.report.Jobs["receiver"] = ScenarioJobReport{Ref: ref}
	require.NoError(t, f.releaseScenarioHold(ctx, rt, "batch"))
	lease, err = controlled.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: one.JobKey, WorkerID: "targeted", Routes: []jobdb.Route{{JobType: "native-probe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, lease)
	lease.StopKeepAlive()
	three := submit()
	require.NotEqual(t, one.JobKey, three.JobKey, "a real acquired receiver must have a successor")
	blocked, err := controlled.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: three.JobKey, WorkerID: "blocked-successor", Routes: []jobdb.Route{{JobType: "native-probe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.Nil(t, blocked, "a successor must not acquire while its predecessor is held for publication")
	require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: one.JobKey}))
	successor, err := controlled.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: three.JobKey, WorkerID: "successor", Routes: []jobdb.Route{{JobType: "native-probe"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, successor, "retired hold must not cover later generations")
	successor.StopKeepAlive()
}

// Force a page boundary without creating hundreds of unrelated native jobs.
// Acquisition still uses the real SQLite backend.
type scenarioPages struct {
	caseRuntime
	pages  map[string]jobdb.ListJobsResponse
	lists  []jobdb.ListJobsRequest
	leases []jobdb.GetJobLeaseRequest
}

func (r *scenarioPages) ListJobs(_ context.Context, q jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.lists = append(r.lists, q)
	return r.pages[q.PageToken], nil
}
func (r *scenarioPages) GetJobLease(ctx context.Context, q jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	r.leases = append(r.leases, q)
	return r.caseRuntime.GetJobLease(ctx, q)
}
func TestScenarioAdmissionPaginationAndFilters(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, closeRT, err := newCaseRuntime(ctx, t.TempDir(), "pages", &RuntimeCase{Backend: "sqlite"})
	require.NoError(t, err)
	defer closeRT()
	_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "pages", Name: "root", Repo: "example.test/pages"})
	require.NoError(t, err)
	var jobs []jobdb.JobSummary
	for _, recipe := range []string{"receiver", "other", "other"} {
		h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "pages", Cell: "root", CellRepo: "example.test/pages", JobType: "probe", Data: jobdb.NewTaskDataOrPanic(map[string]any{}), Metadata: json.RawMessage(`{"recipe":"` + recipe + `","number":2}`)}})
		require.NoError(t, err)
		jobs = append(jobs, jobdb.JobSummary{JobKey: h.JobKey, Cell: "root", CellRepo: "example.test/pages", Metadata: json.RawMessage(`{"recipe":"` + recipe + `","number":2}`)})
	}
	// Held and predicate-ineligible entries must not consume the lease limit.
	skipped := jobs[1]
	skipped.Metadata = json.RawMessage(`{"number":1}`)
	pages := &scenarioPages{caseRuntime: rt, pages: map[string]jobdb.ListJobsResponse{"": {Jobs: []jobdb.JobSummary{jobs[0], skipped}, NextPageToken: "second"}, "second": {Jobs: jobs[1:]}}}
	spec := &Scenario{AdmissionHolds: []AdmissionHold{{ID: "batch", Cell: "root", Recipe: "receiver", Route: jobdb.Route{JobType: "probe"}}}}
	f := &runtimeFixture{c: Case{Runtime: &RuntimeCase{Controls: &RuntimeControls{Scenario: spec}}}, seededRepos: map[string]string{"root": "example.test/pages"}}
	f.initControls()
	q := jobdb.PollWorkRequest{TenantId: "pages", CellNames: []string{"root"}, WorkerID: "paged-worker", Routes: []jobdb.Route{{JobType: "probe"}}, MetadataEquals: []jobdb.MetadataPredicate{{Path: []string{"number"}, Values: []any{2}}}, Limit: 1, LeaseDuration: time.Minute}
	leases, err := (scenarioRuntime{pages, f}).PollWork(ctx, q)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	defer leases[0].StopKeepAlive()
	require.Len(t, pages.lists, 2)
	require.Len(t, pages.leases, 1)
	require.Equal(t, jobs[1].JobKey, pages.leases[0].JobKey)
	require.Equal(t, q.WorkerID, pages.leases[0].WorkerID)
	require.Equal(t, q.Routes, pages.leases[0].Routes)
	require.Equal(t, q.LeaseDuration, pages.leases[0].LeaseDuration)
	for _, r := range pages.lists {
		require.Equal(t, []string{q.TenantId}, r.TenantIds)
		require.Equal(t, q.CellNames, r.CellNames)
		require.Equal(t, q.Routes, r.AvailableRoutes)
	}
	pages.pages = map[string]jobdb.ListJobsResponse{"": {NextPageToken: "loop"}, "loop": {NextPageToken: "loop"}}
	_, err = (scenarioRuntime{pages, f}).PollWork(ctx, q)
	require.ErrorContains(t, err, "repeated cursor")
}
