package compiler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Exercise compiler metadata, live manifest preparation, durable requests,
// task-time reads, actual extension stdin, suspension/resume and offline replay.
func TestConfigurationDefaultExtensionResume(t *testing.T) {
	gate := coreops.NewActivityMappedOpV2[struct{}, struct{}](coreops.OpMetadata{Type: "default_gate"}, func(coreops.OpDependencies, context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	withRegisteredOps(t, extops.GetExecutionOp(), gate)
	for _, backend := range []string{"toy", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			commitHookFile(t, repo, "ops/echo/op.yaml", `name: echo
shell: sh
run: cat
input_schema:
  type: object
  additionalProperties: false
  properties:
    value:
      type: string
      default: '${{ context.config.?value.orValue(null) }}'
output_schema: {type: object}
`)
			rt := newTimeoutReproRuntime(t, backend).WorkflowRuntime
			key := submitGitLifecycle(t, rt, repo, `id: defaults
sequence:
 - id: first
   op: ./ops/echo
 - id: gate
   op: default_gate
 - id: second
   op: ./ops/echo
 - id: override
   op: ./ops/echo
   inputs: {value: explicit}
outputs:
 first: '${{ sequence.first.outputs }}'
 second: '${{ sequence.second.outputs }}'
 override: '${{ sequence.override.outputs }}'
`)
			ctx := celltest.Context(context.Background())
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
			require.NoError(t, err)
			guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
			run := func(includeGate bool) jobworkflow.JobRunOutcome {
				var tasks []jobworkflow.TaskWorker
				for name, w := range ws.TaskWorkers {
					if includeGate || name != "default_gate:default_gate" {
						tasks = append(tasks, guard.WrapTaskWorker(w))
					}
				}
				runnable, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "defaults", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, err := runnable.Run(nil)
				require.NoError(t, err)
				return out
			}
			require.Equal(t, jobworkflow.JobRunSuspended, run(false).Status)
			_, err = rt.(jobdb.ConfigurationAdmin).SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: key.TenantId, Name: "value", Value: json.RawMessage(`"live"`)})
			require.NoError(t, err)
			result := gitLifecycleOutput(t, run(true))
			require.Equal(t, map[string]any{"first": map[string]any{}, "second": map[string]any{"value": "live"}, "override": map[string]any{"value": "explicit"}}, result)
			// A completed first task must not re-read configuration or rerun preparation.
			_, err = rt.(jobdb.ConfigurationAdmin).SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: key.TenantId, Name: "value", Value: json.RawMessage(`false`)})
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(repo))
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			raw, err := replay.GetData()
			require.NoError(t, err)
			var restored map[string]any
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Equal(t, result, restored)
		})
	}
}
