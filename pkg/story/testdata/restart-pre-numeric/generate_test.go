// Copy this file to pkg/story/legacy_generate_test.go in the documented old
// checkout to regenerate the persisted compatibility fixture. It uses that
// revision's restartProbeTask test worker and production compiler.
package story_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

const legacyNumericRecipe = `id: upgrade_numeric
sequence:
 - id: prior
   op: restart_context_probe
   inputs: {value: prior}
 - id: nested
   inputs: {count: '${{ inputs.?count.orValue(0) }}', fraction: 1.25}
   state:
     initial: work
     states:
       work:
         inputs: {count: '${{ inputs.count }}'}
         sequence:
          - id: inner
            op: restart_context_probe
            inputs: {value: inner}
          - id: target
            op: restart_context_probe
            inputs: {value: target}
         outputs: {count: '${{ inputs.count + 1 }}'}
   outputs: {count: '${{ states.work.outputs.count + inputs.count }}', fraction: '${{ inputs.fraction + 0.5 }}'}
outputs: {count: '${{ sequence.nested.outputs.count + 1 }}', fraction: '${{ sequence.nested.outputs.fraction }}'}
`

type legacyNumericInterruption struct {
	jobdb.WorkflowRuntime
	lease  jobdb.ExecutionLease
	cancel context.CancelFunc
	fired  bool
}

func (r *legacyNumericInterruption) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	r.lease = lease
	return lease, err
}
func (r *legacyNumericInterruption) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if err := r.WorkflowRuntime.PutChapter(ctx, req); err != nil {
		return err
	}
	if body, ok := req.Chapter.Body.(jobdb.ExecutionChapter); ok && !r.fired {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if record.Kind == "result" && record.Output != nil && strings.Contains(string(record.Output.Data), `"value":"inner"`) {
			r.fired = true
			if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}); err != nil {
				return err
			}
			r.cancel()
			return fmt.Errorf("fixture worker loss after durable inner result")
		}
	}
	return nil
}

func TestGeneratePreNumericRestartHistory(t *testing.T) {
	dir := os.Getenv("C2J_LEGACY_FIXTURE_DIR")
	require.NotEmpty(t, dir)
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(dir, "jobs.db"), MaxInlineArtifactBytes: 1 << 20})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close(ctx)) }()
	coreops.Register(coreops.NewActivityMappedOpV2[restartProbeInput, map[string]any](coreops.OpMetadata{Type: restartProbeOp}, func(coreops.OpDependencies, context.Context, restartProbeInput) (map[string]any, error) {
		panic("unexpected operation")
	}))
	w := compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{})
	task := &restartProbeTask{}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(db).WithWorkerTenantId("restart-tests").PlusWorkers(w, task).BuildEngine()
	require.NoError(t, err)
	celltest.Create(t, db, "restart-tests", "test", "example.test/test")
	keys := map[string]string{}
	for _, mode := range []string{"completed", "in_progress"} {
		data, err := jobdb.NewTaskData(workflowctl.StartJob{TenantId: "restart-tests", RecipeName: "fixture", Cell: "test", CellRepo: "example.test/test"}, jobdb.NewArtifactFromBytes("fixture"+starter.RecipeArtifactSuffix, []byte(legacyNumericRecipe)))
		require.NoError(t, err)
		handle, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "restart-tests", JobType: "recipe", Data: data, Cell: "test", CellRepo: "example.test/test"})
		require.NoError(t, err)
		keys[mode] = handle.JobKey.JobId
		var rt jobdb.WorkflowRuntime = db
		runCtx, cancel := context.WithCancel(ctx)
		interruption := &legacyNumericInterruption{WorkflowRuntime: db, cancel: cancel}
		if mode == "in_progress" {
			rt = interruption
		}
		run, err := jobworkflow.GetJobForRun(runCtx, rt, jobworkflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: w, TaskWorkers: []jobworkflow.TaskWorker{compiler.NewCellContextTaskWorker(), task}, WorkerID: "pre-fix", LeaseDuration: time.Minute})
		require.NoError(t, err)
		out, err := run.Run(nil)
		cancel()
		if mode == "in_progress" {
			require.Error(t, err)
			require.True(t, interruption.fired)
			job, err := db.GetJob(ctx, handle.JobKey)
			require.NoError(t, err)
			require.Empty(t, job.CompletionStatus)
		} else {
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
		}
	}
	require.Equal(t, int32(5), task.calls.Load())
	raw, err := json.MarshalIndent(keys, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "jobs.json"), raw, 0600))
}
