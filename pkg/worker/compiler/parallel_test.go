package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type parallelTestWorker struct {
	jobworkflow.TaskWorker
	run func(jobworkflow.TaskWorker, context.Context, jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
}

func (worker parallelTestWorker) Run(task jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return worker.RunTask(task.Context(), task, input)
}

func (worker parallelTestWorker) RunTask(ctx context.Context, task jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return worker.run(worker.TaskWorker, ctx, task, input)
}

func TestParallelLaunchErrorClassification(t *testing.T) {
	rejection := &jobdb.AppError{Payload: jobdb.AppErrorPayload{
		Message: "CPU demand exceeds the catalog offering",
		Attrs:   map[string]any{"code": executioncatalog.UnsupportedRequirements},
	}}
	data, err := parallelLaunchError(rejection)
	require.NoError(t, err)
	raw, err := data.GetData()
	require.NoError(t, err)
	var result parallelResult
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Equal(t, parallelResult{Status: "start_failed", Error: rejection.Error()}, result)

	unavailable := jobdb.NewSystemError(jobdb.SystemErrorPayload{Code: executioncatalog.Unavailable, Retryable: true})
	data, err = parallelLaunchError(unavailable)
	require.Nil(t, data)
	require.ErrorIs(t, err, unavailable)
}

func TestParallelRetriesUnavailableLaunchPreflight(t *testing.T) {
	workspaceTestWorker(t)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	var attempts atomic.Int32
	engine, _ := objectEngineAt(t, filepath.Join(t.TempDir(), "parallel.db"), func(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
		if worker.Name() != parallelLaunchTask {
			return worker
		}
		return parallelTestWorker{worker, func(worker jobworkflow.TaskWorker, ctx context.Context, task jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
			if attempts.Add(1) == 1 {
				ctx = executioncatalog.WithPolicy(ctx, executioncatalog.Policy{
					Required: true,
					Scope:    execution.CatalogScope{Tenant: task.JobKey.TenantId},
					Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
						return jobdb.ConfigurationSnapshot{}, errors.New("transient configuration read failure")
					},
				})
			}
			return coretask.RunLocal(worker, ctx, task, input)
		}}
	})
	job := contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out := runObjectRecipe(t, engine, `id: parent
parallel:
  branches:
  - {id: branch, op: workspace_probe}
outputs: {head: '${{ parallel.branch.outputs.head }}'}
`, job)
	require.NotEmpty(t, out["head"])
	require.EqualValues(t, 2, attempts.Load())
}

func TestParallelInheritsTransition(t *testing.T) {
	type echoInput struct {
		Value string `json:"Value"`
	}
	withRegisteredOps(t, ops.NewActivityMappedOpV2[echoInput, map[string]any](ops.OpMetadata{Type: "parallel_transition_echo"}, func(_ ops.OpDependencies, _ context.Context, input echoInput) (map[string]any, error) {
		return map[string]any{"value": input.Value}, nil
	}))
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	job := contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out := runObjectRecipe(t, objectEngine(t), `id: parent
state:
  initial:
    to: compare
    payload: {message: hello, integer: 9007199254740993}
  states:
    compare:
      sequence:
      - id: sequential
        op: parallel_transition_echo
        inputs: {Value: '${{ transition.payload.message + ":" + transition.to + ":" + string(transition.payload.integer) }}'}
      - id: concurrent
        parallel:
          branches:
          - id: echo
            op: parallel_transition_echo
            inputs: {Value: '${{ transition.payload.message + ":" + transition.to + ":" + string(transition.payload.integer) }}'}
        outputs: {value: '${{ parallel.echo.outputs.value }}'}
      outputs:
        sequential: '${{ sequence.sequential.outputs.value }}'
        concurrent: '${{ sequence.concurrent.outputs.value }}'
outputs: {values: '${{ states.compare.outputs }}'}
`, job)
	require.Equal(t, map[string]any{"sequential": "hello:compare:9007199254740993", "concurrent": "hello:compare:9007199254740993"}, out["values"])
}

func TestParallelInheritsScopedExecutionNeeds(t *testing.T) {
	type probeInput struct {
		Name string `json:"Name"`
	}
	withRegisteredOps(t, ops.NewActivityMappedOpV2[probeInput, map[string]any](ops.OpMetadata{Type: "parallel_cpu_probe"}, func(_ ops.OpDependencies, _ context.Context, input probeInput) (map[string]any, error) {
		return map[string]any{"name": input.Name}, nil
	}))
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	seen := make(chan workerops.ActivityInvocationRequest, 3)
	cpu := "8"
	allocation := execution.Allocation{SchemaVersion: 2, Base: "linux", Resources: execution.Resources{CPU: &cpu}}
	engine, _ := objectEngineAtWithAllocation(t, filepath.Join(t.TempDir(), "parallel.db"), allocation, func(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
		if worker.Name() != "parallel_cpu_probe:parallel_cpu_probe" {
			return worker
		}
		return parallelTestWorker{worker, func(worker jobworkflow.TaskWorker, ctx context.Context, task jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
			raw, err := input.GetData()
			if err != nil {
				return nil, err
			}
			var invocation workerops.ActivityInvocationRequest
			if err := json.Unmarshal(raw, &invocation); err != nil {
				return nil, err
			}
			seen <- invocation
			return coretask.RunLocal(worker, ctx, task, input)
		}}
	})
	job := contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	runObjectRecipe(t, engine, `id: parent
execution_needs: {resources: {cpu: 4}}
sequence:
- {id: sequential, op: parallel_cpu_probe, inputs: {Name: sequential}}
- id: concurrent
  parallel:
    branches:
    - {id: inherited, op: parallel_cpu_probe, inputs: {Name: inherited}}
    - id: overridden
      op: parallel_cpu_probe
      inputs: {Name: overridden}
      execution_needs: {resources: {cpu: 2}}
`, job)
	require.Len(t, seen, 3)
	actual := map[string]string{}
	for range 3 {
		invocation := <-seen
		if cpu := invocation.Preparation.Execution.Resources.CPU; cpu != nil {
			actual[invocation.Input["Name"].(string)] = *cpu
		}
	}
	require.Equal(t, map[string]string{"sequential": "4", "inherited": "4", "overridden": "2"}, actual)
}

func TestParallelCollectsBranchOutputs(t *testing.T) {
	ctx := celltest.Context(context.Background())
	type echoInput struct {
		Message string `json:"message"`
		Publish bool   `json:"publish"`
	}
	echo := ops.NewActivityMappedOpV2[echoInput, map[string]any](ops.OpMetadata{Type: "parallel_test_echo"}, func(deps ops.OpDependencies, _ context.Context, input echoInput) (map[string]any, error) {
		if input.Publish {
			if err := deps.AddOutputArtifact(jobdb.NewArtifactFromBytes("payload.txt", []byte(input.Message))); err != nil {
				return nil, err
			}
		}
		return map[string]any{"message": input.Message}, nil
	})
	read := ops.NewActivityMappedOpV2[struct{}, map[string]any](ops.OpMetadata{Type: "parallel_test_read", AcceptsArtifacts: true}, func(deps ops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		for _, artifact := range deps.GetInputArtifacts() {
			if artifact.Name() == "payload.txt" {
				text, err := artifact.Bytes(ctx)
				return map[string]any{"content": string(text)}, err
			}
		}
		return nil, fmt.Errorf("missing branch artifact")
	})
	workspaceTestWorker(t, echo, read)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	engine, _ := objectEngineAt(t, filepath.Join(t.TempDir(), "parallel.db"))
	document, err := recipe.LoadRecipeFromString([]byte(`id: parent
version: "1"
input_schema: {message: {type: string}}
inputs: {message: "${{ inputs.message }}"}
sequence:
- id: joined
  inputs: {text: "${{ inputs.message }}"}
  parallel:
    branches:
    - id: first
      op: parallel_test_echo
      inputs: {message: "${{ inputs.text }}", publish: true}
    - id: second
      op: parallel_test_echo
      inputs: {message: two}
    - id: skipped
      when: "false"
      op: parallel_test_echo
  outputs:
    first: "${{ parallel.first.outputs.message }}"
    second: "{{ parallel.second.outputs.message }}"
    payload: "${{ parallel.first.artifacts['payload.txt'] }}"
- id: consume
  op: parallel_test_read
  artifacts:
    payload.txt: "${{ sequence.joined.outputs.payload }}"
outputs:
  joined: {first: "${{ sequence.joined.outputs.first }}", second: "${{ sequence.joined.outputs.second }}"}
  payload: "${{ sequence.consume.outputs.content }}"
`))
	require.NoError(t, err)
	cellRepo := celltest.Repo(repo)
	celltest.Create(t, engine, "object-tests", "test", cellRepo)
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "parent", Cell: "test", CellRepo: cellRepo, GitRef: "main",
		Inputs: map[string]any{"message": "one"}, JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}, engine, *document)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, ref.Key(), engine))
	result, err := swfutil.JobResult(ctx, engine, ref.Key())
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var output struct {
		Joined  map[string]any
		Payload string
	}
	require.NoError(t, json.Unmarshal(raw, &output))
	require.Equal(t, map[string]any{"first": "one", "second": "two"}, output.Joined)
	require.Equal(t, "one", output.Payload)
	children, err := engine.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{ref.Key().TenantId}, ParentJobIDs: []string{ref.Key().JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, children.Jobs, 2)
}

func TestParallelWaitsForAllAfterWorkerReplacement(t *testing.T) {
	ctx := celltest.Context(context.Background())
	seen := make(chan string, 2)
	type branchInput struct {
		ID string `json:"id"`
	}
	probe := ops.NewActivityMappedOpV2[branchInput, map[string]any](ops.OpMetadata{Type: "parallel_test_hold"}, func(deps ops.OpDependencies, _ context.Context, input branchInput) (map[string]any, error) {
		seen <- deps.JobTool().GetJobKey().JobId
		if input.ID == "fail" {
			return nil, fmt.Errorf("intentional failure")
		}
		deps.SetNextTaskType("test.parallel.hold")
		return map[string]any{"kept": true}, nil
	})
	workspaceTestWorker(t, probe)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	db := filepath.Join(t.TempDir(), "parallel.db")
	engine, stop := objectEngineAt(t, db)
	document, err := recipe.LoadRecipeFromString([]byte(`id: parent
version: "1"
parallel:
  branches:
  - id: held
    op: parallel_test_hold
    inputs: {id: hold}
  - id: failed
    op: parallel_test_hold
    inputs: {id: fail}
catch:
  - when: "true"
    continue:
      outputs:
        branches: "${{ failure.attrs.parallel }}"
`))
	require.NoError(t, err)
	cellRepo := celltest.Repo(repo)
	celltest.Create(t, engine, "object-tests", "test", cellRepo)
	parent, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "parent", Cell: "test", CellRepo: cellRepo, GitRef: "main",
		JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}, engine, *document)
	require.NoError(t, err)
	for range 2 {
		select {
		case <-seen:
		case <-time.After(10 * time.Second):
			t.Fatal("both branches must run before the held branch finishes")
		}
	}
	var held jobdb.JobKey
	var failed jobdb.JobKey
	require.Eventually(t, func() bool {
		children, err := engine.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{parent.Key().TenantId}, ParentJobIDs: []string{parent.Key().JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
		if err != nil || len(children.Jobs) != 2 {
			return false
		}
		for _, child := range children.Jobs {
			if child.NextRoute != nil && child.NextRoute.TaskType == "test.parallel.hold" {
				held = child.JobKey
			}
			if child.Status == jobdb.JobStatusCompleted {
				failed = child.JobKey
			}
		}
		return held.JobId != "" && failed.JobId != ""
	}, 10*time.Second, 10*time.Millisecond)
	control := func() *workerworkflow.SWFWorkflowControl { return &workerworkflow.SWFWorkflowControl{Engine: engine} }
	require.Eventually(t, func() bool {
		inspection, err := control().InspectJob(ctx, parent.Key())
		if err != nil || inspection.BlockingWait == nil {
			return false
		}
		dependencies := inspection.BlockingWait.Dependencies
		if len(dependencies) != 2 || dependencies[0].Condition != jobdb.JobPrereqComplete || dependencies[1].Condition != jobdb.JobPrereqComplete {
			return false
		}
		return dependencies[0].Target.JobID == failed.JobId && dependencies[1].Target.JobID == held.JobId
	}, 10*time.Second, 10*time.Millisecond)
	stop()
	engine, _ = objectEngineAt(t, db)
	require.NoError(t, engine.CancelJob(ctx, jobdb.CancelJob{JobKey: held}))
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, parent.Key(), engine))
	result, err := swfutil.JobResult(ctx, engine, parent.Key())
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var output parallelResults
	require.NoError(t, json.Unmarshal(raw, &output))
	require.Equal(t, "cancelled", output.Branches["held"].Status)
	require.Equal(t, held.JobId, output.Branches["held"].JobID)
	require.Equal(t, "failed", output.Branches["failed"].Status)
	require.Equal(t, failed.JobId, output.Branches["failed"].JobID)
	children, err := engine.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{parent.Key().TenantId}, ParentJobIDs: []string{parent.Key().JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, children.Jobs, 2)
}

func TestParallelRestoresSnapshotAndDiscardsBranchWrites(t *testing.T) {
	workspaceTestWorker(t)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	engine := objectEngine(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out := runObjectRecipe(t, engine, `id: parent
sequence:
- id: write
  op: workspace_probe
  inputs: {write: parent.txt}
- id: join
  parallel:
    branches:
    - id: one
      sequence:
      - id: modify
        const: false
        op: workspace_probe
        inputs: {read: parent.txt, write: branch.txt}
      - id: check
        op: workspace_probe
        inputs: {read: parent.txt, absent: branch.txt}
      outputs: {text: '${{ sequence.check.outputs.text }}', head: '${{ sequence.check.outputs.head }}'}
    - id: two
      op: workspace_probe
      inputs: {read: parent.txt, absent: branch.txt}
  outputs:
    one: '${{ parallel.one.outputs }}'
    two: '${{ parallel.two.outputs }}'
- id: after
  op: workspace_probe
  inputs: {read: parent.txt, absent: branch.txt}
outputs:
  joined: '${{ sequence.join.outputs }}'
  after: '${{ sequence.after.outputs }}'
`, job)
	joined := out["joined"].(map[string]any)
	one := joined["one"].(map[string]any)
	two := joined["two"].(map[string]any)
	require.Equal(t, "test data", one["text"])
	require.Equal(t, one["text"], two["text"])
	require.Equal(t, one["head"], two["head"])
	require.Equal(t, one["head"], out["after"].(map[string]any)["head"])
	_, err := os.Stat(filepath.Join(repo, "parent.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestParallelPinsFloatingWorkspaceBeforeAnyBranchRuns(t *testing.T) {
	workspaceTestWorker(t)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	old, err := common.GetCommitHash(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	engine := objectEngine(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out := runObjectRecipe(t, engine, `id: parent
input_schema: {repo: {type: string}}
inputs: {repo: '${{ inputs.repo }}'}
sequence:
- id: join
  inputs: {repo: '${{ inputs.repo }}'}
  parallel:
    branches:
    - id: one
      op: workspace_probe
      inputs: {advance: '${{ inputs.repo }}'}
    - id: two
      op: workspace_probe
      inputs: {absent: moved}
  outputs:
    one: '${{ parallel.one.outputs.head }}'
    two: '${{ parallel.two.outputs.head }}'
- id: after
  op: workspace_probe
  inputs: {read: moved}
outputs:
  joined: '${{ sequence.join.outputs }}'
  after: '${{ sequence.after.outputs.head }}'
`, job, map[string]any{"repo": repo})
	require.Equal(t, map[string]any{"one": old, "two": old}, out["joined"])
	current, err := common.GetCommitHash(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	require.NotEqual(t, old, current)
	require.Equal(t, current, out["after"])
}

func TestParallelUsesPinnedRecipeSourceForIncludes(t *testing.T) {
	ctx := celltest.Context(context.Background())
	workspaceTestWorker(t)
	definition := t.TempDir()
	workspaceRepo(t, definition, "definition")
	worktree := t.TempDir()
	workspaceHead := workspaceRepo(t, worktree, "workspace")
	require.NoError(t, os.WriteFile(filepath.Join(definition, "root.yaml"), []byte(`id: root
parallel:
  branches:
  - id: include
    include: ./included.yaml
  - id: probe
    op: workspace_probe
    inputs: {read: identity}
outputs:
  included: '${{ parallel.include.outputs }}'
  worktree: '${{ parallel.probe.outputs.head }}'
`), 0600))
	included := "id: included\nsequence: []\noutputs:\n  version: recorded\n  source: '${{ context.recipe_source.ref }}'\n"
	path := filepath.Join(definition, "included.yaml")
	require.NoError(t, os.WriteFile(path, []byte(included), 0600))
	require.NoError(t, runGit(definition, "git", "add", "."))
	require.NoError(t, runGit(definition, "git", "commit", "-m", "add recipes"))
	pin, err := common.GetCommitHash(ctx, definition, "HEAD")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("id: included\nsequence: []\noutputs: {version: moved}\n"), 0600))
	require.NoError(t, runGit(definition, "git", "add", "."))
	require.NoError(t, runGit(definition, "git", "commit", "-m", "move recipe tip"))
	engine := objectEngine(t)
	cellRepo := celltest.Repo(worktree)
	celltest.Create(t, engine, "object-tests", "test", cellRepo)
	selector := fmt.Sprintf("git+file://%s//root.yaml@%s", definition, pin)
	parent, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{RecipeName: selector, TenantId: "object-tests", Cell: "test", CellRepo: cellRepo, GitRef: "main",
		JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: worktree, BaseRef: "main"}}}, engine)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, parent.Key(), engine))
	result, err := swfutil.JobResult(ctx, engine, parent.Key())
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var output struct {
		Included map[string]any
		Worktree string
	}
	require.NoError(t, json.Unmarshal(raw, &output))
	require.Equal(t, map[string]any{"version": "recorded", "source": pin}, output.Included)
	require.Equal(t, workspaceHead, output.Worktree)
}

func TestParallelParentTimeoutDoesNotCancelBranches(t *testing.T) {
	ctx := celltest.Context(context.Background())
	hold := ops.NewActivityMappedOpV2[struct{}, map[string]any](ops.OpMetadata{Type: "parallel_test_hold"}, func(deps ops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
		deps.SetNextTaskType("test.parallel.hold")
		return nil, nil
	})
	workspaceTestWorker(t, hold)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	engine := objectEngine(t)
	document, err := recipe.LoadRecipeFromString([]byte(`id: parent
sequence:
- id: join
  timeout: 3s
  parallel:
    branches:
    - {id: first, op: parallel_test_hold}
    - {id: second, op: parallel_test_hold}
`))
	require.NoError(t, err)
	cellRepo := celltest.Repo(repo)
	celltest.Create(t, engine, "object-tests", "test", cellRepo)
	parent, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "parent", Cell: "test", CellRepo: cellRepo, GitRef: "main",
		JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}, engine, *document)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 10*time.Second, parent.Key(), engine))
	_, err = swfutil.JobResult(ctx, engine, parent.Key())
	require.ErrorContains(t, err, "scope total timed out")
	require.NotContains(t, err.Error(), "parallel branches were unsuccessful")
	children, err := engine.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{parent.Key().TenantId}, ParentJobIDs: []string{parent.Key().JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, children.Jobs, 2)
	for _, child := range children.Jobs {
		require.NotEqual(t, jobdb.JobStatusCancelled, child.Status)
		require.NotNil(t, child.NextRoute)
		require.Equal(t, "test.parallel.hold", child.NextRoute.TaskType)
	}
}

type parallelLaunchFault struct {
	jobworkflow.TaskWorker
	launched chan<- string
	release  <-chan struct{}
	fired    *atomic.Bool
}

func (worker parallelLaunchFault) Run(task jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return worker.RunTask(task.Context(), task, input)
}

func (worker parallelLaunchFault) RunTask(ctx context.Context, task jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var request parallelLaunch
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	if request.BranchID == "rejected" {
		request.Recipe = "sequence: ["
		input = jobdb.NewTaskDataOrPanic(request)
	}
	out, err := coretask.RunLocal(worker.TaskWorker, ctx, task, input)
	if err != nil || request.BranchID != "first" || !worker.fired.CompareAndSwap(false, true) {
		return out, err
	}
	raw, err = out.GetData()
	if err != nil {
		return nil, err
	}
	var result parallelResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	worker.launched <- result.JobID
	select {
	case <-worker.release:
		return nil, jobworkflow.EAGAINWait(task)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestParallelRecoversUnacknowledgedLaunchAndKeepsRejection(t *testing.T) {
	ctx := celltest.Context(context.Background())
	workspaceTestWorker(t)
	repo := t.TempDir()
	old := workspaceRepo(t, repo, "root")
	db := filepath.Join(t.TempDir(), "parallel.db")
	launched := make(chan string, 1)
	release := make(chan struct{})
	var fired atomic.Bool
	engine, _ := objectEngineAt(t, db, func(worker jobworkflow.TaskWorker) jobworkflow.TaskWorker {
		if worker.Name() == parallelLaunchTask {
			return parallelLaunchFault{TaskWorker: worker, launched: launched, release: release, fired: &fired}
		}
		return worker
	})
	document, err := recipe.LoadRecipeFromString([]byte(`id: parent
parallel:
  branches:
  - {id: first, op: workspace_probe}
  - {id: rejected, op: workspace_probe}
  - {id: last, op: workspace_probe}
catch:
  - when: "true"
    continue:
      outputs: {branches: '${{ failure.attrs.parallel }}'}
`))
	require.NoError(t, err)
	cellRepo := celltest.Repo(repo)
	celltest.Create(t, engine, "object-tests", "test", cellRepo)
	parent, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "parent", Cell: "test", CellRepo: cellRepo, GitRef: "main",
		JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}, engine, *document)
	require.NoError(t, err)
	var first string
	select {
	case first = <-launched:
		require.NotEmpty(t, first)
	case <-time.After(10 * time.Second):
		t.Fatal("no durable child submission")
	}
	require.NoError(t, os.WriteFile(filepath.Join(repo, "moved"), []byte("advanced"), 0600))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "advance between launches"))
	close(release)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 15*time.Second, parent.Key(), engine))
	result, err := swfutil.JobResult(ctx, engine, parent.Key())
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var output parallelResults
	require.NoError(t, json.Unmarshal(raw, &output))
	require.Equal(t, first, output.Branches["first"].JobID)
	require.Equal(t, "completed", output.Branches["first"].Status)
	require.Equal(t, old, output.Branches["first"].Outputs["head"])
	require.Equal(t, "start_failed", output.Branches["rejected"].Status)
	require.Empty(t, output.Branches["rejected"].JobID)
	require.Contains(t, output.Branches["rejected"].Error, "yaml")
	require.Equal(t, "completed", output.Branches["last"].Status)
	require.Equal(t, old, output.Branches["last"].Outputs["head"])
	children, err := engine.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{parent.Key().TenantId}, ParentJobIDs: []string{parent.Key().JobId}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	require.Len(t, children.Jobs, 2)
}

type parallelSecretFixture struct{ revision atomic.Int32 }

func (fixture *parallelSecretFixture) ResolveSecrets(_ context.Context, request jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	result := jobdb.SecretResolveResult{Secrets: make(map[string]jobdb.SecretValue)}
	for _, name := range request.Names {
		result.Secrets[name] = jobdb.SecretValue{Value: fmt.Sprintf("fake-private-token-%d", fixture.revision.Load())}
	}
	return result, nil
}

func TestParallelLoadsFreshValuesWithoutWideningPermissions(t *testing.T) {
	ctx, cancel := context.WithCancel(celltest.Context(context.Background()))
	defer cancel()
	provider := &parallelSecretFixture{}
	runtime := toyruntime.New(toyruntime.WithProjects(jobdbcelltest.UnrestrictedProjects{}), toyruntime.WithSecretProvider(provider))
	setConfig := func(value string) error {
		_, err := runtime.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "object-tests", Name: "revision", Value: json.RawMessage(value)})
		return err
	}
	require.NoError(t, setConfig("0"))
	type valueInput struct {
		Token    string `json:"token" secret:"true"`
		Revision int    `json:"revision"`
		Advance  bool   `json:"advance"`
	}
	probe := ops.NewActivityMappedOpV2[valueInput, map[string]any](ops.OpMetadata{Type: "parallel_value_probe"}, func(_ ops.OpDependencies, _ context.Context, input valueInput) (map[string]any, error) {
		match := input.Token == fmt.Sprintf("fake-private-token-%d", input.Revision)
		if input.Advance {
			provider.revision.Store(1)
			if err := setConfig("1"); err != nil {
				return nil, err
			}
		}
		return map[string]any{"valid": match, "revision": input.Revision}, nil
	})
	workspaceTestWorker(t, probe)
	guard := executionruntime.New(runtime, execution.Allocation{SchemaVersion: 1})
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("object-tests").BuildEngine()
	require.NoError(t, err)
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime}
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	control := &workerworkflow.SWFWorkflowControl{Engine: engine}
	workers, err := NewRecipeWorkerWithOptions(ops.NewServiceDepsBuilder().WithWorkflowControl(control).Build(), registry, RecipeJobWorkerOptions{WrapTaskWorker: guard.WrapTaskWorker})
	require.NoError(t, err)
	require.NoError(t, engine.RegisterWorkers(workers))
	go engine.Run(ctx)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	out := runObjectRecipe(t, engine, `id: parent
perms: {limit: {jobdb: {secrets: {resolve: [token]}}}}
sequence:
- id: before
  op: parallel_value_probe
  inputs:
    token: '${{ context.secrets.token }}'
    revision: '${{ context.config.revision }}'
    advance: true
- id: joined
  inputs:
    token: '${{ context.secrets.token }}'
    revision: '${{ context.config.revision }}'
  parallel:
    branches:
    - id: allowed
      op: parallel_value_probe
      inputs: {token: '${{ inputs.token }}', revision: '${{ inputs.revision }}'}
    - id: denied
      op: parallel_value_probe
      inputs: {token: '${{ context.secrets.denied }}', revision: 1}
  catch:
    - when: "true"
      continue:
        outputs: {results: '${{ failure.attrs.parallel }}'}
outputs:
  before: '${{ sequence.before.outputs }}'
  results: '${{ sequence.joined.outputs.results }}'
`, job)
	require.Equal(t, map[string]any{"valid": true, "revision": float64(0)}, out["before"])
	results := out["results"].(map[string]any)
	allowed := results["allowed"].(map[string]any)
	require.Equal(t, "completed", allowed["status"])
	require.Equal(t, map[string]any{"valid": true, "revision": float64(1)}, allowed["outputs"])
	denied := results["denied"].(map[string]any)
	require.Equal(t, "failed", denied["status"])
	require.Contains(t, denied["error"], "enclosing limit")
	jobs, err := engine.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"object-tests"}, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
	require.NoError(t, err)
	for _, finished := range jobs.Jobs {
		run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: finished.JobKey, IncludeInputs: true, IncludeOutputs: true})
		require.NoError(t, err)
		raw, err := json.Marshal(run)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "fake-private-token-")
	}
}
