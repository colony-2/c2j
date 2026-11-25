package compiler

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type hookConfigRuntime struct {
	*toy.Runtime
	revisions   []string
	unavailable bool
}

func (runtime *hookConfigRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := runtime.Runtime.GetJobLease(ctx, req)
	if err != nil || lease == nil {
		return lease, err
	}
	return hookConfigLease{lease, runtime}, nil
}

type hookConfigLease struct {
	jobdb.ExecutionLease
	runtime *hookConfigRuntime
}

func (lease hookConfigLease) GetConfiguration(ctx context.Context) (jobdb.ConfigurationSnapshot, error) {
	if lease.runtime.unavailable {
		return jobdb.ConfigurationSnapshot{}, jobdb.ErrConfigurationUnavailable
	}
	snapshot, err := lease.ExecutionLease.GetConfiguration(ctx)
	if err == nil {
		lease.runtime.revisions = append(lease.runtime.revisions, snapshot.Revision)
	}
	return snapshot, err
}

func TestExecutionHookPolicySharesConfigurationAndReplaysDecision(t *testing.T) {
	for _, scenario := range []string{"single-execution", "resumed", "invalid", "unreadable", "skipped"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("C2J_WRITE_EXECUTION_HOOK_POLICY", "true")
			ctx := celltest.Context(context.Background())
			const tenant = "hooks"
			const firstHook = ".c2/recipes/hooks/build/verify.yaml"
			const secondHook = ".c2/hooks/second.yaml"
			repository := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repository, "hook-repo")
			const definition = `id: verify
sequence:
  - id: probe
    op: hook_configuration_probe
    inputs: {flag: '${{ context.config.flag }}'}
`
			commitHookFile(t, repository, firstHook, definition)
			commitHookFile(t, repository, secondHook, definition)
			identity, err := hookpolicy.Identity(celltest.Repo(repository))
			require.NoError(t, err)
			runtime := &hookConfigRuntime{Runtime: toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))}
			set := func(name, value string) {
				_, err := runtime.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: tenant, Name: name, Value: json.RawMessage(value)})
				require.NoError(t, err)
			}
			set("flag", `"first"`)
			entry := hookpolicy.Entry{
				Perms: permissions.Spec{JobDB: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"token"}}}},
				Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{"token": {Mode: "candidate"}}},
			}
			policy, err := json.Marshal(hookpolicy.Document{Version: 1, Hooks: map[string]hookpolicy.Entry{firstHook: entry, secondHook: entry}})
			require.NoError(t, err)
			set(hookpolicy.TenantKey, string(policy))
			amend := func() {
				set("flag", `"second"`)
				document := hookpolicy.Document{Version: 1, Repository: identity, Hooks: map[string]hookpolicy.Entry{secondHook: hookpolicy.Empty()}}
				raw, err := json.Marshal(document)
				require.NoError(t, err)
				set(hookpolicy.CellKey(identity), string(raw))
			}
			switch scenario {
			case "invalid", "skipped":
				set(hookpolicy.TenantKey, `{"version":99}`)
			case "unreadable":
				runtime.unavailable = true
			}
			type input struct {
				Flag string `json:"flag"`
			}
			type probeResult struct {
				Flag    string
				Granted bool
			}
			var probes []probeResult
			probe := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "hook_configuration_probe"}, func(_ coreops.OpDependencies, ctx context.Context, input input) (map[string]any, error) {
				probes = append(probes, probeResult{input.Flag, len(permissions.FromContext(ctx).Grant().Secrets.Resolve) > 0})
				if scenario == "single-execution" && len(probes) == 2 {
					amend()
				}
				return map[string]any{"flag": input.Flag}, nil
			})
			barrier := coreops.NewActivityMappedOpV2[struct{}, struct{}](coreops.OpMetadata{Type: "hook_configuration_barrier"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (struct{}, error) {
				return struct{}{}, nil
			})
			workers := workspaceTestWorker(t, probe, barrier)
			guard := executionruntime.New(runtime, execution.Allocation{SchemaVersion: 1})
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId(tenant).PlusWorkers(workers.JobWorker).BuildEngine()
			require.NoError(t, err)
			engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime}
			body := `id: build
sequence:
  - id: ordinary
    op: hook_configuration_probe
    inputs: {flag: '${{ context.config.flag }}'}
  - id: first
    include: cell://.c2/recipes/hooks/build/verify.yaml
  - id: boundary
    op: hook_configuration_barrier
  - id: second
    include: cell://.c2/hooks/second.yaml
`
			if scenario == "invalid" || scenario == "unreadable" {
				body = "id: build\nsequence:\n- id: verify\n  include: cell://" + firstHook + "\n"
			} else if scenario == "skipped" {
				body = "id: build\nsequence:\n- id: verify\n  when: 'false'\n  include: cell://" + firstHook + "\n"
			}
			rec, err := recipe.LoadRecipeFromString([]byte(body))
			require.NoError(t, err)
			celltest.Create(t, runtime, tenant, "test", celltest.Repo(repository))
			job, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: tenant, RecipeName: "build", Cell: "test", CellRepo: celltest.Repo(repository), GitRef: "main", JobContext: contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repository, BaseRef: "main"}}}, engine, *rec)
			require.NoError(t, err)
			run := func(omitBarrier bool) jobworkflow.JobRunOutcome {
				var tasks []jobworkflow.TaskWorker
				for name, worker := range workers.TaskWorkers {
					if !omitBarrier || name != "hook_configuration_barrier:hook_configuration_barrier" {
						tasks = append(tasks, guard.WrapTaskWorker(worker))
					}
				}
				runnable, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: job.Key(), JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, err)
				outcome, err := runnable.Run(nil)
				require.NoError(t, err)
				return outcome
			}
			if scenario == "resumed" {
				require.Equal(t, jobworkflow.JobRunSuspended, run(true).Status)
				require.Equal(t, []probeResult{{"first", false}, {"first", true}}, probes)
				require.Len(t, runtime.revisions, 1)
				amend()
				commitHookFile(t, repository, firstHook, "this edited verify hook is not valid recipe YAML")
			}
			outcome := run(false)
			if scenario == "invalid" || scenario == "unreadable" {
				require.NotEqual(t, jobworkflow.JobRunCompleted, outcome.Status)
				require.NotNil(t, outcome.JobError)
				require.Empty(t, probes)
				return
			}
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			if scenario == "skipped" {
				history, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: job.Key(), IncludeInputs: true, IncludeOutputs: true})
				require.NoError(t, err)
				for _, task := range executionTaskRuns(history) {
					require.NotContains(t, []string{hookPolicyTaskType, hookResolveTaskType}, task.TaskType)
				}
				require.Empty(t, probes)
				return
			}
			want := probeResult{"first", true}
			scopes := []string{"tenant", "tenant"}
			var revisions []string
			if scenario == "resumed" {
				require.Len(t, runtime.revisions, 2)
				want = probeResult{"second", false}
				scopes[1] = "cell"
				revisions = runtime.revisions
			} else {
				require.Len(t, runtime.revisions, 1)
				revisions = []string{runtime.revisions[0], runtime.revisions[0]}
			}
			require.Equal(t, []probeResult{{"first", false}, {"first", true}, want}, probes)
			history, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: job.Key(), IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			var decisions []hookDecision
			for _, task := range executionTaskRuns(history) {
				require.NotEqual(t, hookPolicyTaskType, task.TaskType)
				if task.TaskType != hookResolveTaskType {
					continue
				}
				for _, attempt := range task.Attempts {
					if attempt.Input != nil {
						require.NotContains(t, string(attempt.Input.Data), `"policy"`)
					}
					if attempt.Output == nil {
						continue
					}
					require.NotContains(t, string(attempt.Output.Data), `"hooks"`)
					decoded, err := decodeTaskOutput(attempt.Output.Data)
					require.NoError(t, err)
					raw, err := json.Marshal(decoded.Activity.OpOutput)
					require.NoError(t, err)
					var result hookResolution
					require.NoError(t, json.Unmarshal(raw, &result))
					if result.Decision != nil {
						decisions = append(decisions, *result.Decision)
					}
				}
			}
			require.Len(t, decisions, 2)
			for index, decision := range decisions {
				require.Equal(t, scopes[index], decision.Scope)
				require.Equal(t, revisions[index], decision.Revision)
			}
			runtime.unavailable = true
			set(hookpolicy.TenantKey, `{"version":99}`)
			_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: job.Key()})
			require.NoError(t, err)
		})
	}
}
