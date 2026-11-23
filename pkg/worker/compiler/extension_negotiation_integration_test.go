package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/internal/executiontest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/executionruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runNegotiatingRecipe(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey, allocation execution.Allocation, supplied bool, policies ...executioncatalog.Policy) (jobworkflow.JobRunOutcome, error) {
	t.Helper()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	guard := executionruntime.New(rt, allocation)
	if len(policies) > 0 {
		guard.BaseCatalog = policies[0]
	}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: engine}).Build()
	ws, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{Allocation: allocation, StageExecution: guard.Stage, WrapTaskWorker: guard.WrapTaskWorker})
	require.NoError(t, err)
	var tasks []jobworkflow.TaskWorker
	for _, task := range ws.TaskWorkers {
		tasks = append(tasks, task)
	}
	req := jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "negotiation", LeaseDuration: time.Minute}
	ctx := celltest.Context(context.Background())
	if supplied {
		lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "recipe"}}, WorkerID: req.WorkerID, LeaseDuration: time.Minute})
		require.NoError(t, err)
		// SQLite can return a typed nil lease when the native job is not ready.
		if !assert.NotNil(t, lease) {
			info, infoErr := rt.GetJob(ctx, key)
			run, runErr := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key})
			t.Fatalf("native lease unavailable at %s: job=%+v job_error=%v schedule=%+v schedule_error=%v", time.Now().UTC().Format(time.RFC3339Nano), info, infoErr, run.Job, runErr)
		}
		return guard.RunWithLease(ctx, lease, req, nil)
	}
	run, err := jobworkflow.GetJobForRun(ctx, guard, req)
	if err != nil {
		return jobworkflow.JobRunOutcome{}, err
	}
	return run.Run(nil)
}

func TestExtensionNegotiationUsesExistingYieldAndReplay(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			counter := filepath.Join(t.TempDir(), "runs")
			manifest := fmt.Sprintf(`name: negotiated
shell: sh
run: |
  payload=$(cat)
  printf '%%s %%s\n' "$C2J_CURRENT_INVOCATION_HASH" "$payload" >> %q
  case "$C2J_EXECUTION_ALLOCATION" in
    *'"docker"'*) printf '{"output":{"ready":true,"env":%%s}}' "$C2J_EXECUTION_ALLOCATION";;
    *) printf '{"execution_required":%%s}' "$payload"; exit 75;;
  esac
input_schema: {type: object}
output_schema: {type: object, required: [ready]}
`, counter)
			commitHookFile(t, repo, "ops/negotiated/op.yaml", manifest)
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
execution: {base: '*'}
sequence:
 - id: first
   op: ./ops/negotiated
   retry: {maximum_attempts: 1}
   catch:
    - when: true
      continue: {outputs: {caught: true}}
   inputs: {capabilities: [docker]}
outputs: {result: '${{ sequence.first.outputs }}'}
`)
			ctx := celltest.Context(context.Background())
			// Seed unrelated state to verify lossless demand publication.
			lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{{JobType: "recipe"}}, WorkerID: "seed", LeaseDuration: time.Minute})
			require.NoError(t, err)
			revision := lease.ClientPayloadRevision()
			require.NoError(t, lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}, ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: json.RawMessage(`{"application":{"integer":9007199254740993}}`), ExpectedRevision: &revision}}))
			allocation := execution.Allocation{SchemaVersion: 2, Base: "ubuntu-26"}
			for i := 0; i < 2; i++ {
				out, err := runNegotiatingRecipe(t, rt, key, allocation, i == 1)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status, "%v", out.JobError)
				handoff, err := ExecutionHandoffFromOutcome(out)
				require.NoError(t, err)
				require.NotNil(t, handoff)
				require.Equal(t, []string{"docker"}, handoff.Demand.Effective.Capabilities)
				require.Equal(t, "capabilities", handoff.Mismatches[0].Field)
			}
			allocation.Base, allocation.Capabilities = "macos-123", []string{"docker"}
			out, err := runNegotiatingRecipe(t, rt, key, allocation, true)
			require.NoError(t, err)
			result := gitLifecycleOutput(t, out)["result"].(map[string]any)
			require.Equal(t, true, result["ready"], "handoff must bypass catch and failure retry")
			require.Equal(t, "macos-123", result["env"].(map[string]any)["base"])
			log, err := os.ReadFile(counter)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(log)), "\n")
			require.Len(t, lines, 3)
			require.Equal(t, lines[0], lines[1], "same logical invocation and input after handoff")
			require.Equal(t, lines[0], lines[2])
			info, err := rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.Contains(t, string(info.ClientPayload), "9007199254740993")
			// Read-only replay must not launch the extension, even without its source.
			require.NoError(t, os.RemoveAll(repo))
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			after, err := os.ReadFile(counter)
			require.NoError(t, err)
			require.Equal(t, log, after)
		})
	}
}

func TestExtensionEnvironmentContainsEncompassingPackages(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\nexit 0\n' > "$UV_TOOL_BIN_DIR/tool"
chmod +x "$UV_TOOL_BIN_DIR/tool"
`), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	jobPackages := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "negotiation_job_packages"}, func(deps coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
		return map[string]any{}, coreops.SuspendExecution(deps, execution.Requirements{Packages: []string{"uv:job"}})
	})
	withRegisteredOps(t, extops.GetExecutionOp(), jobPackages)
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "source")
	commitHookFile(t, repo, ".c2/config.yaml", "packages: [uv:repository]\n")
	commitHookFile(t, repo, "ops/info/op.yaml", `name: info
shell: sh
run: 'printf ''{"output":{"environment":%s}}'' "$C2J_EXECUTION_ALLOCATION"'
dependencies: [uv:extension]
input_schema: {type: object}
output_schema: {type: object}
`)
	rt := newTimeoutReproRuntime(t, "toy").ConsumerRuntime
	key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
execution: {packages: [uv:recipe]}
sequence:
 - id: add_job_packages
   op: negotiation_job_packages
 - id: nested
   execution_needs: {packages: [uv:node]}
   sequence:
    - id: info
      op: ./ops/info
   outputs: {environment: '${{ sequence.info.outputs.environment }}'}
 - id: later
   op: ./ops/info
outputs:
 nested: '${{ sequence.nested.outputs.environment }}'
 later: '${{ sequence.later.outputs.environment }}'
`)
	out, err := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, false)
	require.NoError(t, err)
	result := gitLifecycleOutput(t, out)
	decode := func(key string) execution.Environment {
		raw, err := json.Marshal(result[key])
		require.NoError(t, err)
		var env execution.Environment
		require.NoError(t, json.Unmarshal(raw, &env))
		return env
	}
	require.ElementsMatch(t, []string{"uv:repository", "uv:recipe", "uv:node", "uv:job", "uv:extension"}, decode("nested").Packages)
	require.ElementsMatch(t, []string{"uv:repository", "uv:recipe", "uv:job", "uv:extension"}, decode("later").Packages)
}

func TestExtensionSatisfiedRequirementsAreAnOperationError(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "source")
	commitHookFile(t, repo, "ops/already-met/op.yaml", `name: already-met
shell: sh
run: 'printf ''{"execution_required":{"base":"macos*","capabilities":["docker"]}}''; exit 75'
input_schema: {type: object}
output_schema: {type: object}
`)
	rt := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
	key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
op: ./ops/already-met
catch:
 - when: true
   continue: {outputs: {message: '${{ failure.message }}'}}
`)
	out, err := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2, Base: "macos-123", Capabilities: []string{"docker"}}, false)
	require.NoError(t, err)
	require.Nil(t, out.Handoff)
	require.Contains(t, gitLifecycleOutput(t, out)["message"], "already satisfied")
}

func TestExecutionBaseCatalogExtensionNegotiationRefusal(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, backend := range []string{"sqlite", "remote"} {
		for _, kind := range []string{"base", "capability", "cpu", "memory"} {
			t.Run(backend+"/"+kind, func(t *testing.T) {
				request := `{"execution_required":{"base":"macos-sequoia-15.7.7-arm64"}}`
				want := executioncatalog.Unsupported
				switch kind {
				case "capability":
					request = `{"execution_required":{"base":"linux","capabilities":["special"]}}`
					want = executioncatalog.UnsupportedRequirements
				case "cpu":
					request = `{"execution_required":{"base":"linux","resources":{"cpu":"16"}}}`
					want = executioncatalog.UnsupportedRequirements
				case "memory":
					request = `{"execution_required":{"base":"linux","resources":{"memory":"64Gi"}}}`
					want = executioncatalog.UnsupportedRequirements
				}
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "source")
				commitHookFile(t, repo, "ops/refused/op.yaml", `name: refused
shell: sh
run: |
  printf '%s' '`+request+`'
  exit 75
input_schema: {type: object}
output_schema: {type: object}
`)
				rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
				key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
execution: {base: '*'}
sequence:
 - id: negotiate
   op: ./ops/refused
   retry: {maximum_attempts: 3}
   catch:
    - when: "failure.code == '`+want+`' && failure.retryable == false"
      continue:
       outputs:
        code: "${{ failure.code }}"
        choices: "${{ failure.attrs.named_bases }}"
outputs: {result: '${{ sequence.negotiate.outputs }}'}
`)
				now := time.Now().UTC()
				c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test-jobdb", Tenant: key.TenantId}, Complete: true, Offerings: executiontest.Offerings("linux", "macos-15-arm64"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
				cpuMax, memoryMax := "8", "32Gi"
				c.Offerings[0].Resources.CPU = &execution.ResourceRule{State: "range", Max: &cpuMax}
				c.Offerings[0].Resources.Memory = &execution.ResourceRule{State: "range", Max: &memoryMax}
				raw, err := json.Marshal(c)
				require.NoError(t, err)
				_, err = rt.(jobdb.ConfigurationAdmin).SetTenantConfigurationValue(celltest.Context(context.Background()), jobdb.SetTenantConfigurationValueRequest{TenantID: key.TenantId, Name: execution.BaseCatalogKey, Value: raw})
				require.NoError(t, err)
				out, err := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: execution.SchemaVersion, Base: "linux"}, false, executioncatalog.Policy{Required: true, Scope: c.Scope})
				require.NoError(t, err)
				result := gitLifecycleOutput(t, out)["result"].(map[string]any)
				require.Equal(t, want, result["code"])
				require.Equal(t, []any{"linux", "macos-15-arm64"}, result["choices"])
				info, err := rt.GetJob(celltest.Context(context.Background()), key)
				require.NoError(t, err)
				require.Empty(t, info.ClientPayload)
				require.Nil(t, out.Handoff)
			})
		}
	}
}
