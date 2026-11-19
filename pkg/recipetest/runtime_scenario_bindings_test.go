package recipetest

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/c2j/internal/scenariotest"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestScenarioBindingRejectsUnprovedIdentity(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, closeRT, err := newCaseRuntime(ctx, t.TempDir(), "binding", &RuntimeCase{Backend: "sqlite"})
	require.NoError(t, err)
	defer closeRT()
	_, err = rt.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "binding", Name: "root", Repo: "example.test/binding"})
	require.NoError(t, err)
	submit := func() jobref.Ref {
		h, e := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "binding", Cell: "root", CellRepo: "example.test/binding", JobType: "probe", Data: jobdb.NewTaskDataOrPanic(map[string]any{})}})
		require.NoError(t, e)
		r, e := jobref.FromHandle(h)
		require.NoError(t, e)
		return r
	}
	caller, target := submit(), submit()
	q := TaskSelection{TaskType: "recipes.run:start", NodePath: "caller/queue", Attempt: 1}
	receipt := scenariotest.Submission{Caller: caller.JobID, InvocationID: "queue-a", NodePath: q.NodePath, TaskType: q.TaskType, Attempt: 1, Ref: target, Index: 1}
	fixture := func() *runtimeFixture {
		s := &Scenario{Jobs: map[string]ScenarioJob{"caller": {Root: true}, "receiver": {Submission: &SubmissionSelection{Caller: "caller", Task: q, Index: 1}}}, Outcomes: map[string]ScenarioOutcome{"caller": {Status: "completed"}, "receiver": {Status: "completed"}}}
		f := &runtimeFixture{root: t.TempDir(), rootJobID: caller.JobID, c: Case{Runtime: &RuntimeCase{Controls: &RuntimeControls{Scenario: s}}}}
		f.initControls()
		return f
	}
	t.Run("existing job is not a receipt", func(t *testing.T) {
		f := fixture()
		require.NoError(t, f.bindScenarioJobs(ctx, rt, "binding"))
		require.NotContains(t, f.scenario.report.Jobs, "receiver")
	})
	for _, field := range []string{"tenant", "cell", "goal", "repository"} {
		t.Run(field, func(t *testing.T) {
			f := fixture()
			r := receipt
			switch field {
			case "tenant":
				r.Ref.TenantID = "foreign"
			case "cell":
				r.Ref.Cell = "foreign"
			case "goal":
				r.Ref.GoalID = "foreign"
			case "repository":
				r.Ref.CellRepo = "foreign"
			}
			f.scenario.report.Receipts = []scenariotest.Submission{r}
			require.ErrorContains(t, f.bindScenarioJobs(ctx, rt, "binding"), "native identity mismatch")
		})
	}
	t.Run("ambiguous invocation", func(t *testing.T) {
		f := fixture()
		second := receipt
		second.InvocationID = "queue-b"
		f.scenario.report.Receipts = []scenariotest.Submission{receipt, second}
		require.ErrorContains(t, f.bindScenarioJobs(ctx, rt, "binding"), "ambiguous")
	})
	t.Run("conflicting aliases", func(t *testing.T) {
		f := fixture()
		f.scenario.report.Jobs["other"] = ScenarioJobReport{Ref: target}
		f.c.Runtime.Controls.Scenario.Outcomes["other"] = ScenarioOutcome{Status: "cancelled"}
		f.scenario.report.Receipts = []scenariotest.Submission{receipt}
		require.ErrorContains(t, f.bindScenarioJobs(ctx, rt, "binding"), "conflicting outcomes")
	})
	t.Run("boundary already dispatched", func(t *testing.T) {
		f := fixture()
		f.c.Runtime.Controls.Scenario.DispatchHolds = []DispatchHold{{ID: "late", Job: "receiver", Task: q}}
		f.initScenario()
		f.scenario.report.Receipts = []scenariotest.Submission{receipt}
		f.report.Controls.Dispatch = []DispatchAttempt{{Attempt: dispatchtest.Attempt{JobID: target.JobID, Name: q.TaskType, NodePath: q.NodePath, InvocationID: "task", Attempt: 1}}}
		require.ErrorContains(t, f.bindScenarioJobs(ctx, rt, "binding"), "bound after target dispatched")
	})
	t.Run("denied submit has no binding", func(t *testing.T) {
		f := fixture()
		finish, e := f.beginSubmission(ctx, receipt)
		require.NoError(t, e)
		require.ErrorContains(t, finish(jobref.Ref{}, errors.New("denied")), "denied")
		require.Empty(t, f.scenario.report.Receipts)
		require.NoError(t, f.bindScenarioJobs(ctx, rt, "binding"))
		require.NotContains(t, f.scenario.report.Jobs, "receiver")
	})
	for _, state := range []string{"finished", "denied", "running"} {
		t.Run("forbidden dispatch before binding/"+state, func(t *testing.T) {
			f := fixture()
			f.c.Runtime.Controls.Dispatch = &DispatchPolicy{Forbid: []DispatchMatcher{{Job: "receiver", Kind: "task", Name: "publish", NodePath: "receiver/publish"}}}
			a := dispatchtest.Attempt{Kind: "task", Name: "publish", NodePath: "receiver/publish", JobID: caller.JobID, InvocationID: "peer-publish", Attempt: 1}
			finish, err := f.beginDispatch(ctx, a)
			require.NoError(t, err)
			finish(nil)
			require.NoError(t, f.bindScenarioJobs(ctx, rt, "binding"))
			require.NotContains(t, f.scenario.report.Jobs, "receiver")
			f.scenario.report.Receipts = []scenariotest.Submission{receipt}
			a.JobID, a.InvocationID = target.JobID, "receiver-publish"
			finish, err = f.beginDispatch(ctx, a)
			require.NoError(t, err, "the native target is not yet bound")
			switch state {
			case "finished":
				finish(nil)
			case "denied":
				finish(jobdb.ErrLeasePermissionDenied)
			}
			err = f.bindScenarioJobs(ctx, rt, "binding")
			require.ErrorContains(t, err, "forbidden task dispatch publish")
			require.Contains(t, err.Error(), target.JobID, "a peer's matching dispatch must remain allowed")
			if state == "running" {
				finish(nil)
			}
			result := CaseRunResult{Status: "passed"}
			f.scenario.report.Complete = true
			f.finishControls(&result)
			require.Equal(t, "failed", result.Status, "final acceptance must also reconcile earlier attempts")
			require.Contains(t, result.FailureReason, "forbidden task dispatch publish")
			require.False(t, f.report.Controls.Complete)
			require.False(t, f.scenario.report.Complete)
			require.True(t, f.report.Controls.Dispatch[1].Entered, "retain the actual earlier entry, not a retrospective denial")
		})
	}
}
