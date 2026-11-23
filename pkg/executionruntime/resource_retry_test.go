package executionruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/projectcatalog"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type resourceTask struct {
	wf.TaskWorker
	runs       atomic.Int32
	invocation string
	rejects    int32
	support    bool
}

func (w *resourceTask) RunTask(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	if w.invocation == "" {
		w.invocation = tc.InvocationID
	}
	if w.invocation != tc.InvocationID || tc.AttemptNumber != 1 {
		return nil, errors.New("resource restart changed invocation or recipe attempt")
	}
	w.support = resourceretry.Supported(local)
	if w.runs.Add(1) <= w.rejects {
		return nil, resourceretry.Restart("preparation_diagnostic", "preparation")
	}
	return w.TaskWorker.Run(tc, in)
}

type resourceLease struct {
	jobdb.RenewableExecutionLease
	*resourceObservation
}
type resourceObservation struct {
	receipt     jobdb.ResourceBackoffReceipt
	request     jobdb.ResourceBackoffRequest
	requestedAt time.Time
	reject      error
	lostAck     bool
}

func (l *resourceLease) LeaseSchemaHash() string {
	return l.RenewableExecutionLease.(interface{ LeaseSchemaHash() string }).LeaseSchemaHash()
}
func (l *resourceLease) LeaseToken() string {
	if port, ok := l.RenewableExecutionLease.(interface{ LeaseToken() string }); ok {
		return port.LeaseToken()
	}
	return ""
}
func (l *resourceLease) LeaseWorkerID() string {
	if port, ok := l.RenewableExecutionLease.(interface{ LeaseWorkerID() string }); ok {
		return port.LeaseWorkerID()
	}
	return ""
}
func (l *resourceLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, err := l.RenewableExecutionLease.Renew(ctx)
	if err != nil {
		return nil, err
	}
	return &resourceLease{next, l.resourceObservation}, nil
}
func (l *resourceLease) RescheduleResourceBackoff(ctx context.Context, req jobdb.ResourceBackoffRequest) (jobdb.ResourceBackoffReceipt, error) {
	l.request = req
	l.requestedAt = time.Now()
	if l.reject != nil {
		return jobdb.ResourceBackoffReceipt{}, l.reject
	}
	r, err := l.RenewableExecutionLease.(jobdb.ResourceBackoffLease).RescheduleResourceBackoff(ctx, req)
	l.receipt = r
	if l.lostAck && err == nil {
		return jobdb.ResourceBackoffReceipt{}, errors.New("lost backoff acknowledgment")
	}
	return r, err
}

func claimResourceLease(t *testing.T, f *fixture) *resourceLease {
	t.Helper()
	l, err := f.runtime.GetJobLease(celltest.Context(context.Background()), jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "resource-test", LeaseDuration: time.Minute, Routes: []jobdb.Route{{JobType: "recipe"}}})
	require.NoError(t, err)
	require.NotNil(t, l)
	return &resourceLease{l.(jobdb.RenewableExecutionLease), &resourceObservation{}}
}

func TestResourceRetryDurableRecovery(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			f := newFixture(t, backend, "2Gi", nil)
			waiting := &resourceTask{TaskWorker: f.tasks[1], rejects: 2}
			tasks := []wf.TaskWorker{f.tasks[0], waiting}
			var previous jobdb.ResourceBackoffReceipt
			for i := 1; i <= 2; i++ {
				claimed := claimResourceLease(t, f)
				rt := New(f.runtime, allocation("2Gi"))
				out, err := runFixtureLease(t, f, rt, claimed, tasks)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunSuspended, out.Status)
				require.Empty(t, rt.leases)
				require.True(t, waiting.support)
				require.EqualValues(t, i, claimed.receipt.Count)
				require.Equal(t, waiting.invocation+"/attempt:1", claimed.receipt.Resume.Invocation)
				require.EqualValues(t, 1, claimed.receipt.Resume.Attempt)
				require.Equal(t, jobdb.DefaultResourceBackoffPolicy(), claimed.request.Policy)
				info, err := f.runtime.GetJob(ctx, f.key)
				require.NoError(t, err)
				require.Equal(t, jobdb.JobStatusAwaitingFuture, info.Status)
				require.NotNil(t, info.ExecutionState.ResourceResume)
				require.Equal(t, info.ClientPayloadRevision, info.ExecutionState.ResourceResume.PayloadRevision)
				view := execution.Inspect(nil, info.ClientPayload)
				require.NotNil(t, view.Demand, "staged execution demand must commit with the wait")
				require.Equal(t, "2Gi", *view.Demand.Effective.Resources.Memory)
				if i == 1 {
					require.NotNil(t, claimed.request.ClientPayloadUpdate)
				}
				require.EqualValues(t, 1, f.first.Load(), "completed work must remain cached")
				require.Zero(t, f.second.Load())
				early, err := f.runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: f.key, WorkerID: "early", LeaseDuration: time.Minute, Routes: []jobdb.Route{{JobType: "recipe"}}})
				require.NoError(t, err)
				require.Nil(t, early, "resource wait must block every reacquisition")
				remaining := time.Until(claimed.receipt.WakeAt)
				if i == 2 {
					require.Equal(t, previous.Resume.Invocation, claimed.receipt.Resume.Invocation)
					require.Greater(t, claimed.receipt.WakeAt.Sub(claimed.requestedAt), 3*time.Second, "the second backend wait must exceed the first wait's 2.4-second maximum")
				}
				previous = claimed.receipt
				time.Sleep(remaining + 30*time.Millisecond)
			}
			claimed := claimResourceLease(t, f)
			out, err := runFixtureLease(t, f, New(f.runtime, allocation("2Gi")), claimed, tasks)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
			require.EqualValues(t, 3, waiting.runs.Load())
			require.EqualValues(t, 1, f.first.Load())
			require.EqualValues(t, 1, f.second.Load())
		})
	}
}

func TestResourceRetryInfrastructureFailures(t *testing.T) {
	for _, mode := range []string{"rejected", "lost acknowledgment", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			backend := "sqlite"
			if mode == "unsupported" {
				backend = "toy"
			}
			f := newFixture(t, backend, "2Gi", nil)
			waiting := &resourceTask{TaskWorker: f.tasks[0], rejects: 1}
			claimed := claimResourceLease(t, f)
			if mode == "rejected" {
				claimed.reject = errors.New("backend rejected resource wait")
			}
			claimed.lostAck = mode == "lost acknowledgment"
			_, err := runFixtureLease(t, f, New(f.runtime, allocation("2Gi")), claimed, []wf.TaskWorker{waiting})
			require.Error(t, err, "uncertain scheduling must stop as infrastructure failure")
			require.Zero(t, f.first.Load())
			if mode == "unsupported" {
				require.ErrorIs(t, err, jobdb.ErrResourceBackoffUnsupported)
				require.False(t, waiting.support)
			}
			if mode == "lost acknowledgment" {
				require.EqualValues(t, 1, claimed.receipt.Count)
			}
		})
	}
}

// Exercise the whole reader-to-scheduler path with actual subprocess capture,
// workspace restore/persistence, native recipe catches and a reopened SQLite DB.
func TestResourceRetryExtensionDurableRecipe(t *testing.T) {
	ctx := celltest.Context(context.Background())
	previous := coreops.List()
	t.Cleanup(func() { coreops.Replace(previous...) })
	root, trigger := t.TempDir(), filepath.Join(t.TempDir(), "recovered")
	require.NoError(t, os.Mkdir(filepath.Join(root, "op"), 0700))
	control := `{"control":{"type":"resource_retry","version":1,"reason":"resource_temporarily_unavailable","source":"local_spawn","phase":"launch","replay":"restart_from_input"}}`
	run := fmt.Sprintf(`test "$C2J_RESOURCE_RETRY_CONTROL" = 1 || exit 9; if test ! -f '%s'; then touch '%s'; printf '%%s' '%s'; exit 1; fi; printf '%%s' '{"output":{"ready":true}}'`, trigger, trigger, control)
	require.NoError(t, os.WriteFile(filepath.Join(root, "op/op.yaml"), []byte(fmt.Sprintf("name: resource\nshell: sh\nrun: %q\ninput_schema: {type: object}\noutput_schema: {type: object}\n", run)), 0600))
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.com"}, {"add", "."}, {"commit", "-m", "resource fixture"}} {
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	var prior atomic.Int32
	coreops.Register(coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "resource_prior"}, func(deps coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
		prior.Add(1)
		return nil, os.WriteFile(filepath.Join(deps.WorktreePath(), "prior.txt"), []byte("durable"), 0600)
	}))
	coreops.Register(extensions.GetExecutionOp())
	r, err := recipe.LoadRecipeFromString([]byte(`id: resource-recipe
sequence:
 - id: prior
   op: resource_prior
 - id: extension
   op: extension_execution
   inputs: {selector: ./op}
   catch:
    - continue: {outputs: {caught: true}}
outputs:
 ready: "${{ sequence.extension.outputs.ready }}"
`))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "resource.db")
	base, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
	require.NoError(t, err)
	admin := jobdb.WithProjectAdministration(ctx, func(context.Context) error { return nil })
	installationDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	inventory, err := sqlite.InspectProjectInstallation(admin, installationDB, projectcatalog.InstallationDigest([]byte("isolated resource test: fresh SQLite, empty default secrets and blob store, no external writers")))
	require.NoError(t, err)
	require.NoError(t, sqlite.InstallProjectCatalog(admin, installationDB, inventory, []projectcatalog.Seed{{ID: "c2", Name: "c2"}}))
	require.NoError(t, installationDB.Close())
	celltest.Create(t, base, "c2", "resource", celltest.Repo(root))
	engine, err := wf.NewEngineBuilder().WithRuntime(base).BuildEngine()
	require.NoError(t, err)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "resource"}, GitBase: contextual.GitBaseContext{BaseRepo: root, BaseRef: "main"}}
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "c2", RecipeName: "resource-recipe", JobContext: job, GitRef: "main", Cell: "resource", CellRepo: celltest.Repo(root)}, jobdbschema.WorkflowEngine{Engine: engine, Registry: base}, *r)
	require.NoError(t, err)
	runJob := func() wf.JobRunOutcome {
		guard := New(base, execution.Allocation{SchemaVersion: execution.SchemaVersion})
		registry, err := workerops.NewActivityRegistry()
		require.NoError(t, err)
		workers, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{WrapTaskWorker: guard.WrapTaskWorker, StageExecution: guard.Stage})
		require.NoError(t, err)
		var tasks []wf.TaskWorker
		for _, task := range workers.TaskWorkers {
			tasks = append(tasks, task)
		}
		handle, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "resource-recipe", LeaseDuration: time.Minute})
		require.NoError(t, err)
		out, err := handle.Run(nil)
		require.NoError(t, err)
		return out
	}
	out := runJob()
	require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
	require.EqualValues(t, 1, prior.Load())
	info, err := base.GetJob(ctx, ref.Key())
	require.NoError(t, err)
	require.NotNil(t, info.ExecutionState.ResourceResume)
	chapters, err := base.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: ref.Key()})
	require.NoError(t, err)
	checkpoints := 0
	for _, chapter := range chapters {
		if body, ok := chapter.Body.(jobdb.ExecutionChapter); ok {
			record, err := jobdb.DecodeExecutionRecord(body.Record)
			require.NoError(t, err)
			require.Empty(t, record.ErrorKind, "a resource wait must not commit a failed task")
			require.Empty(t, record.Error)
			if record.Resource != nil {
				checkpoints++
			}
		}
	}
	require.Equal(t, 1, checkpoints)
	require.NoError(t, base.Close(ctx))
	base, err = sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
	require.NoError(t, err)
	defer base.Close(ctx)
	// Default first wait is at most 2.4 seconds. Real time is deliberately used;
	// no process/resource exhaustion or scheduler state is fabricated.
	time.Sleep(2500 * time.Millisecond)
	out = runJob()
	require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
	require.EqualValues(t, 1, prior.Load(), "reopen must reuse the committed prior task")
	var result map[string]any
	require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &result))
	require.Equal(t, true, result["ready"], "the normal success result must survive recovery")
}

// An extension wait and a resource restart share the current payload but keep
// their original task identity and deadline across both scheduler handoffs.
func TestResourceRetryPreservesExtensionWait(t *testing.T) {
	for _, expires := range []bool{false, true} {
		t.Run(fmt.Sprintf("expires=%v", expires), func(t *testing.T) {
			budget := jobdb.Duration(30 * time.Second)
			if expires {
				budget = jobdb.Duration(1500 * time.Millisecond)
			}
			past := time.Now().UTC().Add(-time.Hour)
			f := newExtensionWaitFixture(t, "sqlite", extensionwait.Request{Version: 1, Until: &past}, jobdb.RunPolicy{InvocationTimeout: &budget, Retry: jobdb.RetryPolicy{MaximumAttempts: 1}})
			out, err := f.run(t)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunSuspended, out.Status)
			gate := f.gate(t)
			require.NotNil(t, gate.Deadline)
			waitReady(t, gate)
			f.task.run = func(context.Context) error { return resourceretry.Restart("local_spawn", "launch") }
			out, err = f.run(t)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
			require.Equal(t, gate, f.gate(t), "resource handoff must preserve the original wait deadline and binding")
			info, err := f.rt.GetJob(waitTestContext(context.Background()), f.key)
			require.NoError(t, err)
			require.NotNil(t, info.ExecutionState.ResourceResume)
			require.Equal(t, gate.attemptID(), info.ExecutionState.ResourceResume.Invocation)
			// The default first backoff is at most 2.4 seconds, including jitter.
			time.Sleep(2500 * time.Millisecond)
			f.task.run = nil
			out, err = f.run(t)
			require.NoError(t, err)
			if expires {
				require.Equal(t, wf.JobRunFailed, out.Status)
				var timeout jobdb.TimeoutError
				require.ErrorAs(t, out.JobError, &timeout)
				require.Equal(t, jobdb.TimeoutScopeInvocation, timeout.Payload.Scope)
				require.Len(t, f.task.identities, 2, "expired original deadline must prevent another task body")
			} else {
				require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Len(t, f.task.identities, 3)
				require.Nil(t, f.gate(t), "completion must retire the extension gate")
			}
			for _, identity := range f.task.identities {
				require.Equal(t, f.task.identities[0], identity)
			}
		})
	}
}
