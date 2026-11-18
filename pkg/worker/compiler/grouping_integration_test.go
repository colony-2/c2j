package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Dispatch is explicit: both contributions commit before any receiver can lease.
func TestGroupingRealReceiverSQLiteAndHTTP(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var runtime jobdb.WorkflowRuntime = db
			if transport == "http" {
				handler := remote.NewServer(db)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handler.ServeHTTP(w, r.WithContext(celltest.Context(r.Context())))
				}))
				t.Cleanup(server.Close)
				runtime, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			celltest.Create(t, runtime, "tenant", "test", celltest.Repo(repo))
			guard := executionruntime.New(runtime, execution.Allocation{SchemaVersion: 1})
			engine, err := wf.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("tenant").BuildEngine()
			require.NoError(t, err)
			eng := jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime.(jobdb.JobSchemaRegistry)}
			require.NoError(t, jobdbschema.Register(ctx, runtime.(jobdb.JobSchemaRegistry), "tenant"))
			source, err := runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "source", Cell: "test", Data: jobdb.NewTaskDataOrPanic(nil, jobdb.NewArtifactFromBytes("same.txt", []byte("first")))}})
			require.NoError(t, err)
			source2, err := runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "source", Cell: "test", Data: jobdb.NewTaskDataOrPanic(nil, jobdb.NewArtifactFromBytes("same.txt", []byte("second")))}})
			require.NoError(t, err)
			first := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: source.JobKey.JobId, TaskOrdinal: 0, Name: "same.txt", SizeBytes: 5})
			first.Name = "aliased"
			second := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: source2.JobKey.JobId, TaskOrdinal: 0, Name: "same.txt", SizeBytes: 6})
			var fetches atomic.Int32
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fetches.Add(1); _, _ = w.Write([]byte("external")) }))
			defer external.Close()
			refs := []artifacts.Ref{first, {Kind: artifacts.RefKindValue, Name: "inline", Format: "text", Value: json.RawMessage(`"value"`)}, artifacts.NewExternalRef("remote", external.URL, false), {Kind: artifacts.RefKindValue, Name: "evil.recipe.yaml", Format: "text", Value: json.RawMessage(`"invalid recipe"`)}}
			marker := filepath.Join(t.TempDir(), "first-attempt")
			command := fmt.Sprintf(`cat "${{ context.environment.op.inbox }}"/{first,second,inline,remote,creator}.txt; if test ! -f %q; then touch %q; exit 7; fi`, marker, marker)
			rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf(`id: batch
version: "1"
input_schema:
 config: {type: string, required: true}
inputs:
 config: '${{ inputs.config }}'
sequence:
 - id: read
   op: command_execution
   const: true
   retry: {maximum_attempts: 2, initial_interval: 1ns}
   artifacts:
    first.txt: '${{ context.grouping.appends[0].artifacts[0] }}'
    second.txt: '${{ context.grouping.appends[1].artifacts[0] }}'
    inline.txt: '${{ context.grouping.appends[0].artifacts[1] }}'
    remote.txt: '${{ context.grouping.appends[0].artifacts[2] }}'
    creator.txt: '${{ context.artifacts["start/foo"] }}'
   inputs:
    run: %q
outputs:
 text: '${{ sequence.read.outputs.stdout }}'
 count: '${{ context.grouping.appends.size() }}'
 first: '${{ context.grouping.appends[0].content.pr }}'
 second: '${{ context.grouping.appends[1].content.pr }}'
 config: '${{ inputs.config }}'
 isolated: '${{ !("evil.recipe.yaml" in context.artifacts) }}'
`, command)))
			require.NoError(t, err)
			withRegisteredOps(t, commandop.GetOp())
			start := workflowctl.StartJob{TenantId: "tenant", Cell: "test", RecipeName: "batch", GitRef: "main", Inputs: map[string]any{"config": "creator"}, JobContext: contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("start/foo", []byte("creator"))}}
			spec := grouping.Spec{Present: true, Value: map[string]any{"key": "shared", "content": map[string]any{"pr": 1}, "artifacts": refs}}
			a, err := starter.StartOrAppendRecipeJob(ctx, start, spec, eng, *rec)
			require.NoError(t, err)
			spec.Value = map[string]any{"key": "shared", "content": map[string]any{"pr": 2}, "artifacts": []artifacts.Ref{second}}
			start.Inputs["config"] = "joiner"
			start.RecipeName = "different"
			b, err := starter.StartOrAppendRecipeJob(ctx, start, spec, eng, *rec)
			require.NoError(t, err)
			require.Equal(t, a, b)
			require.Zero(t, fetches.Load())
			ctl := &workerworkflow.SWFWorkflowControl{Engine: eng}
			deps := ops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			ws, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{WrapTaskWorker: guard.WrapTaskWorker})
			require.NoError(t, err)
			tasks := []wf.TaskWorker{}
			for _, w := range ws.TaskWorkers {
				tasks = append(tasks, w)
			}
			handle, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: a.Key(), JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "receiver", LeaseDuration: time.Minute})
			require.NoError(t, err)
			run, err := handle.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, run.Status, "%v", run.JobError)
			result := run.Output
			var got map[string]any
			require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &got))
			output := got
			require.Equal(t, "firstsecondvalueexternalcreator", output["text"])
			require.EqualValues(t, 2, output["count"])
			require.Equal(t, "creator", output["config"])
			require.Equal(t, true, output["isolated"])
			require.EqualValues(t, 1, output["first"])
			require.EqualValues(t, 2, output["second"])
			t.Run("result collection", func(t *testing.T) {
				collected, err := swfutil.JobResult(ctx, eng, a.Key())
				require.NoError(t, err)
				require.JSONEq(t, string(result.GetDataOrPanic()), string(collected.GetDataOrPanic()))
			})
			require.NoError(t, eng.RegisterWorkers(ws))
			replayed, err := eng.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: a.Key()})
			require.NoError(t, err)
			require.JSONEq(t, string(result.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			require.EqualValues(t, 2, fetches.Load(), "both actual attempts bind bytes; replay must not fetch or execute again")
			history, err := jobdb.GetJobRun(ctx, runtime, jobdb.GetJobRunRequest{JobKey: a.Key()})
			require.NoError(t, err)
			var point string
			for _, event := range history.Execution {
				if event.Record.Kind == "task" && strings.Contains(event.Record.ID, `scope:"op:read"`) {
					point = event.Record.ID
					break
				}
			}
			require.NotEmpty(t, point)
			restarted, err := runtime.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: a.Key(), ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: point}, Cell: "test"}})
			require.NoError(t, err)
			handle, err = wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: restarted.JobKey, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "restarted-receiver", LeaseDuration: time.Minute})
			require.NoError(t, err)
			rerun, err := handle.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, rerun.Status, "%v", rerun.JobError)
			var restartedOutput map[string]any
			require.NoError(t, json.Unmarshal(rerun.Output.GetDataOrPanic(), &restartedOutput))
			require.Contains(t, output, metrics.JobOutputKey)
			require.Contains(t, restartedOutput, metrics.JobOutputKey)
			// A restart executes new attempts and measures their own CPU/timing.
			// The replay check above still compares the complete original output.
			delete(output, metrics.JobOutputKey)
			delete(restartedOutput, metrics.JobOutputKey)
			require.Equal(t, output, restartedOutput, "restart must retain ordered content, aliases and original artifact identities")
		})
	}
}

func TestGroupingHTTPRegistersMissingSchema(t *testing.T) {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
	require.NoError(t, err)
	defer db.Close(ctx)
	handler := remote.NewServer(db)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(celltest.Context(r.Context())))
	}))
	defer server.Close()
	rt, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	engine, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	eng := jobdbschema.WorkflowEngine{Engine: engine, Registry: rt}
	_, err = starter.StartOrAppendRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", Cell: "test", RecipeName: "batch"}, grouping.Spec{Present: true, Value: map[string]any{"key": "cold", "content": map[string]any{}}}, eng)
	require.NoError(t, err, "a definitive schema miss must register and retry through the same submission route")
}

func TestGroupingOrdinaryArtifactCompatibility(t *testing.T) {
	withRegisteredOps(t, commandop.GetOp())
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, `id: parent
sequence:
- id: read
  op: command_execution
  artifacts: {creator.txt: '${{ context.artifacts["start/foo"] }}'}
  inputs: {run: 'cat "${{ context.environment.op.inbox }}/creator.txt"'}
outputs:
  text: '${{ sequence.read.outputs.stdout }}'
  grouped: '${{ has(context.grouping) }}'
  ref: '${{ context.artifacts["start/foo"] }}'
`)
			f.start.Artifacts = []jobdb.Artifact{jobdb.NewArtifactFromBytes("start/foo", []byte("ordinary bytes"))}
			ref := f.startParent(t)
			out := f.result(t, ref)
			require.Equal(t, "ordinary bytes", out["text"])
			require.Equal(t, false, out["grouped"])
			raw, err := json.Marshal(out["ref"])
			require.NoError(t, err)
			var artifact artifacts.Ref
			require.NoError(t, json.Unmarshal(raw, &artifact))
			key, ok := artifact.StoredKey()
			require.True(t, ok)
			require.Equal(t, "start/foo", key.Name)
			require.Equal(t, ref.JobID, key.JobId)
		})
	}
}
