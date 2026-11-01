package executionruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func capabilityAllocation(names ...string) execution.Allocation {
	return execution.Allocation{SchemaVersion: 1, Capabilities: names}
}

func TestCapabilitiesScopedHandoffAndListing(t *testing.T) {
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			source := `id: capabilities
execution: {capabilities: [base]}
execution_needs: {capabilities: [outer]}
sequence:
 - id: first
   op: needs_a
 - id: nested
   execution_needs: {capabilities: [inner]}
   sequence:
    - id: second
      execution_needs: {capabilities: [leaf]}
      op: needs_b
 - id: sibling
   op: needs_c
`
			f, tasks := newNodeFixture(t, backend, source)
			r, err := recipe.LoadRecipeFromString([]byte(source))
			require.NoError(t, err)
			submitNodeRecipe(t, f, *r, starter.StartRecipeJobOptions{ExecutionRequirements: execution.Requirements{Capabilities: []string{"job"}}})
			outer := capabilityAllocation("base", "job", "outer")
			out, event := runNode(t, f, outer, f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Equal(t, []string{"base", "inner", "job", "leaf", "outer"}, event.Demand.Effective.Capabilities)
			require.Len(t, event.Mismatches, 2)
			require.Equal(t, outer.Capabilities, event.Allocation.Capabilities)
			require.EqualValues(t, 1, tasks[0].runs.Load())
			require.Zero(t, tasks[1].runs.Load())
			info, err := f.runtime.GetJob(context.Background(), f.key)
			require.NoError(t, err)
			view := execution.Inspect(nil, info.ClientPayload)
			require.Equal(t, event.Demand.Effective.Capabilities, view.Demand.Effective.Capabilities)
			_, event = runNode(t, f, outer, f.tasks)
			require.False(t, event.Published, "identical demand is not republished")
			full := capabilityAllocation("base", "inner", "job", "leaf", "outer", "extra")
			out, event = runNode(t, f, full, f.tasks[:2])
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Nil(t, event, "missing sibling worker uses ordinary handoff")
			info, err = f.runtime.GetJob(context.Background(), f.key)
			require.NoError(t, err)
			view = execution.Inspect(nil, info.ClientPayload)
			require.Equal(t, outer.Capabilities, view.Demand.Effective.Capabilities, "nested contributions must not leak")
			out, event = runNode(t, f, outer, f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event, "cached nested task must not require its old capabilities")
			for _, task := range tasks {
				require.EqualValues(t, 1, task.runs.Load())
			}
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(f.runtime).WithWorkerTenantId("tenant").PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: f.key, JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err, "fully cached replay needs no allocation")
			for _, task := range tasks {
				require.EqualValues(t, 1, task.runs.Load())
			}
		})
	}
}

func TestCapabilitiesDurableDirectiveRestart(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f, tasks := newNodeFixture(t, backend, `id: directive
sequence:
 - id: discover
   op: needs_a
 - id: consume
   op: needs_b
 - id: finish
   op: needs_c
`)
			tasks[0].patch = &execution.Requirements{Capabilities: []string{"discovered"}}
			out, event := runNode(t, f, capabilityAllocation(), f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Equal(t, []string{"discovered"}, event.Demand.Effective.Capabilities)
			require.EqualValues(t, 1, tasks[0].runs.Load())
			require.Zero(t, tasks[1].runs.Load())
			// A fresh attempt must reconstruct the cached directive and cannot borrow
			// allocation facts from either the demand or a previous executor.
			out, event = runNode(t, f, capabilityAllocation("discovered"), f.tasks[:2])
			require.Nil(t, event)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			out, event = runNode(t, f, capabilityAllocation(), f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Equal(t, []string{"discovered"}, event.Demand.Effective.Capabilities)
			require.Empty(t, event.Allocation.Capabilities)
			require.Zero(t, tasks[2].runs.Load())
			out, event = runNode(t, f, capabilityAllocation("discovered"), f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			run, err := jobdb.GetJobRun(context.Background(), f.runtime, jobdb.GetJobRunRequest{JobKey: f.key})
			require.NoError(t, err)
			var scopeID string
			for _, e := range run.Execution {
				if e.Record.Kind == "scope" && strings.Contains(e.Record.ID, `scope:"op:finish"`) {
					scopeID = e.Record.ID
				}
			}
			require.NotEmpty(t, scopeID)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(f.runtime).WithWorkerTenantId("tenant").PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			ref, err := starter.RestartRecipeJob(context.Background(), jobdbschema.WorkflowEngine{Engine: engine, Registry: f.runtime.(jobdb.JobSchemaRegistry)}, f.key, scopeID, nil, starter.RestartOptions{Goal: "new", RequestID: "capability-restart"})
			require.NoError(t, err)
			f.key = ref.Key()
			out, event = runNode(t, f, capabilityAllocation(), f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Equal(t, []string{"discovered"}, event.Demand.Effective.Capabilities)
			require.EqualValues(t, 1, tasks[2].runs.Load())
			out, event = runNode(t, f, capabilityAllocation("discovered"), f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			require.EqualValues(t, 1, tasks[0].runs.Load())
			require.EqualValues(t, 1, tasks[1].runs.Load())
			require.EqualValues(t, 2, tasks[2].runs.Load())
		})
	}
}

func TestCapabilitiesIncludeRestoresParent(t *testing.T) {
	f, tasks := newNodeFixture(t, "toy", "id: seed\nsequence: []\n")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "child.yaml"), []byte("id: included\nexecution_needs: {capabilities: [included]}\nsequence:\n - id: inside\n   op: needs_a\n"), 0600))
	r, err := recipe.LoadRecipeFromString([]byte("id: parent\nexecution_needs: {capabilities: [parent]}\nsequence:\n - id: include\n   include: ./child.yaml\n   perms: {limit: {jobdb: {}}}\n - id: sibling\n   op: needs_b\n"))
	require.NoError(t, err)
	expanded, err := compiler.ResolveInlineRecipes(context.Background(), *r, compiler.InlineResolutionOptions{RootFile: filepath.Join(dir, "root.yaml")})
	require.NoError(t, err)
	submitNodeRecipe(t, f, expanded.Recipe)
	_, event := runNode(t, f, capabilityAllocation("parent"), f.tasks)
	require.Equal(t, []string{"included", "parent"}, event.Demand.Effective.Capabilities)
	_, event = runNode(t, f, capabilityAllocation("included", "parent"), f.tasks[:1])
	require.Nil(t, event)
	out, event := runNode(t, f, capabilityAllocation("parent"), f.tasks)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
	require.Nil(t, event)
	require.EqualValues(t, 1, tasks[0].runs.Load())
	require.EqualValues(t, 1, tasks[1].runs.Load())
}

func TestPreCapabilityJobsResumeAndReplay(t *testing.T) {
	// The recipe, directive, allocation, submission and published demand all use
	// the old encoding without capabilities. Exercise persisted task replay too.
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			var patch execution.Requirements
			require.NoError(t, json.Unmarshal([]byte(`{"resources":{"memory":"4Gi"}}`), &patch))
			f := newFixture(t, backend, "2Gi", &patch)
			out, event := f.run(t, "2Gi", f.tasks)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			require.Empty(t, event.Demand.Effective.Capabilities)
			info, err := f.runtime.GetJob(context.Background(), f.key)
			require.NoError(t, err)
			listed, err := f.runtime.ListJobs(context.Background(), jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, JobKeys: []jobdb.JobKey{f.key}})
			require.NoError(t, err)
			require.Len(t, listed.Jobs, 1)
			require.NotContains(t, string(listed.Jobs[0].Metadata), "capabilities")
			require.NotContains(t, string(info.ClientPayload), "capabilities")
			view := execution.Inspect(nil, info.ClientPayload)
			require.Equal(t, "specified", view.Status)
			require.Equal(t, 3, view.Demand.SchemaVersion)
			var a execution.Allocation
			require.NoError(t, json.Unmarshal([]byte(`{"schema_version":1,"resources":{"memory":"4Gi"}}`), &a))
			out, event = runNode(t, f, a, f.tasks)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			require.Nil(t, event)
			require.EqualValues(t, 1, f.first.Load())
			require.EqualValues(t, 1, f.second.Load())
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(f.runtime).WithWorkerTenantId("tenant").PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(context.Background(), jobworkflow.ReplayRunRequest{JobKey: f.key, JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
		})
	}
}
