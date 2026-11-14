package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type hookDecisionContext struct {
	yield bool
	jobworkflow.ExecutionContext
	builds *int
	reject bool
	call   *jobworkflow.TaskCall
}

func (c hookDecisionContext) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	if c.call != nil {
		*c.call = call
	}
	return c.ExecutionContext.DoTask(call, func(ctx context.Context) (jobdb.TaskData, error) {
		(*c.builds)++
		if c.yield {
			return nil, c.ExecutionContext.(interface {
				Yield(context.Context, jobworkflow.YieldRequest) error
			}).Yield(ctx, jobworkflow.YieldRequest{Reschedule: jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "hook-decision"}}})
		}
		if c.reject {
			return nil, fmt.Errorf("input builder must not run")
		}
		return build(ctx)
	})
}

type hookDecisionWorker struct{ legacy bool }

func (hookDecisionWorker) Name() string { return hookResolveTaskType }
func (w hookDecisionWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	ctx := permissions.WithAccess(tc.Context(), permissions.NewAccess(tc.Context(), permissions.JobDB{}, nil, nil))
	if w.legacy {
		var req workerops.ActivityInvocationRequest
		if err := json.Unmarshal(in.GetDataOrPanic(), &req); err != nil {
			return nil, err
		}
		delete(req.Input, "skip_absent_workspace") // Emulate the old worker ignoring an unknown input.
		in = jobdb.NewTaskDataOrPanic(req)
	}
	return (hookResolveWorker{}).RunTask(ctx, tc, in)
}
func decisionRequest(t *testing.T, repo, head, phase string) workerops.ActivityInvocationRequest {
	t.Helper()
	id, err := hookpolicy.Identity(repo)
	require.NoError(t, err)
	return workerops.ActivityInvocationRequest{
		GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: "main", ResolvedBaseHash: head, ParentHash: head, PersistHash: head},
		Input:          map[string]any{"repository": id, "hook": ".c2/hooks/check.yaml", "source_commit": head, "phase": phase, "source_hooks": "from_current"},
	}
}
func decodeHookDecision(t *testing.T, data jobdb.TaskData) hookResolution {
	t.Helper()
	out, err := decodeTaskOutput(data.GetDataOrPanic())
	require.NoError(t, err)
	raw, err := json.Marshal(out.Activity.OpOutput)
	require.NoError(t, err)
	var result hookResolution
	require.NoError(t, json.Unmarshal(raw, &result))
	return result
}
func TestHookAbsentResolutionDecision(t *testing.T) {
	for _, mode := range []string{"absent", "present", "malformed", "source-error"} {
		for _, opt := range []bool{false, true} {
			for _, phase := range []string{"pin", "load"} {
				t.Run(fmt.Sprintf("%s/opt=%t/%s", mode, opt, phase), func(t *testing.T) {
					repo := filepath.Join(t.TempDir(), "repo")
					head := workspaceRepo(t, repo, "A")
					if mode == "present" {
						head = commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nsequence: []\noutputs: {status: present}\n")
					}
					if mode == "malformed" {
						head = commitHookFile(t, repo, ".c2/hooks/check.yaml", "sequence: [")
					}
					req := decisionRequest(t, repo, head, phase)
					req.Input["skip_absent_workspace"] = opt
					if mode == "source-error" {
						req.GitTaskContext.BaseRepo = filepath.Join(t.TempDir(), "missing")
						id, _ := hookpolicy.Identity(req.GitTaskContext.BaseRepo)
						req.Input["repository"] = id
					}
					ctx := permissions.WithAccess(testBackground(), permissions.NewAccess(testBackground(), permissions.JobDB{}, nil, nil))
					out, err := (hookResolveWorker{}).RunTask(ctx, jobworkflow.TaskContext{}, jobdb.NewTaskDataOrPanic(req))
					if mode == "source-error" || (mode == "malformed" && phase == "load") {
						require.Error(t, err)
						require.Nil(t, out)
						return
					}
					require.NoError(t, err)
					result := decodeHookDecision(t, out)
					require.Equal(t, mode == "absent" && phase == "load", result.Absent)
					require.Equal(t, opt && result.Absent, result.SkipAbsentWorkspace)
					raw, err := json.Marshal(result)
					require.NoError(t, err)
					if !result.SkipAbsentWorkspace {
						require.NotContains(t, string(raw), "skip_absent_workspace")
					}
					var roundtrip hookResolution
					require.NoError(t, json.Unmarshal(raw, &roundtrip))
					require.Equal(t, result, roundtrip)
				})
			}
		}
	}
}

func TestHookAbsentDecisionCommandAndStoredInputs(t *testing.T) {
	for _, compiler := range []string{"legacy", "new"} {
		for _, worker := range []string{"legacy", "new"} {
			for _, frontier := range []string{"completed", "waiting", "descriptor"} {
				t.Run(compiler+"/"+worker+"/"+frontier, func(t *testing.T) {
					repo := filepath.Join(t.TempDir(), "repo")
					head := workspaceRepo(t, repo, "A")
					req := decisionRequest(t, repo, head, "load")
					original, err := json.Marshal(req)
					require.NoError(t, err)
					rt := newTimeoutReproRuntime(t, "sqlite")
					builds := 0
					reject := false
					upgraded := false
					yieldBuild := frontier == "descriptor"
					var call jobworkflow.TaskCall
					w := jobworkflow.ExecutionWorker{JobType: "hook-decision", Execute: func(c jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
						c = hookDecisionContext{ExecutionContext: c, builds: &builds, reject: reject, call: &call, yield: yieldBuild}
						if compiler == "legacy" && !upgraded {
							return workflow.ValueCall(c, "hook:test:load", hookResolveTaskType, jobdb.RunPolicy{}, req)
						}
						return callHookResolution(c, "hook:test:load", req, true)
					}}
					key := probeSubmit(t, rt, w)
					tasks := []jobworkflow.TaskWorker{hookDecisionWorker{legacy: worker == "legacy"}}
					if frontier == "waiting" {
						tasks = nil
					}
					out := probeRun(t, rt, key, w, tasks...)
					require.JSONEq(t, string(original), string(call.Command))
					require.NotContains(t, string(call.Command), "skip_absent_workspace")
					require.Equal(t, 1, builds)
					require.Equal(t, "hook:test:load", call.Site)
					require.Equal(t, hookResolveTaskType, call.TaskType)
					require.EqualValues(t, 1, call.ContractVersion)
					require.Equal(t, jobdb.RunPolicy{}, call.Policy)
					// All upgrades replay with the new compiler, including a legacy input.
					upgraded = true
					yieldBuild = false
					reject = frontier != "descriptor"
					before, err := rt.ListChapters(testBackground(), jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, err)
					engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(w).BuildEngine()
					require.NoError(t, err)
					replayed, err := engine.ReplayJobRun(testBackground(), jobworkflow.ReplayRunRequest{JobKey: key})
					if frontier == "completed" {
						require.NoError(t, err)
						require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
						result := decodeHookDecision(t, replayed)
						require.Equal(t, compiler == "new" && worker == "new", result.SkipAbsentWorkspace)
						require.Equal(t, 1, builds)
					} else if frontier == "waiting" {
						require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
						var miss jobworkflow.ReplayCacheMissError
						require.ErrorAs(t, err, &miss)
						var stored workerops.ActivityInvocationRequest
						require.NoError(t, json.Unmarshal(miss.Input.GetDataOrPanic(), &stored))
						require.Equal(t, compiler == "new", stored.Input["skip_absent_workspace"] == true)
						require.Equal(t, 1, builds, "waiting input must bypass the new builder")
					} else {
						require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
						var miss jobworkflow.ReplayCacheMissError
						require.ErrorAs(t, err, &miss)
						var fresh workerops.ActivityInvocationRequest
						require.NoError(t, json.Unmarshal(miss.Input.GetDataOrPanic(), &fresh))
						require.Equal(t, true, fresh.Input["skip_absent_workspace"])
						require.Equal(t, 2, builds, "descriptor-only replay must construct fresh input")
					}
					after, err := rt.ListChapters(testBackground(), jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, err)
					require.Equal(t, before, after)
					if frontier != "completed" {
						resumed := probeRun(t, rt, key, w, hookDecisionWorker{legacy: worker == "legacy"})
						require.Equal(t, jobworkflow.JobRunCompleted, resumed.Status, "%v", resumed.JobError)
						require.Equal(t, (compiler == "new" || frontier == "descriptor") && worker == "new", decodeHookDecision(t, resumed.Output).SkipAbsentWorkspace)
						wantBuilds := 1
						if frontier == "descriptor" {
							wantBuilds = 3
						}
						require.Equal(t, wantBuilds, builds)
					}
					stableBuilds := builds
					// Both an accidental opt-in in the command and a genuine source change
					// must fail descriptor matching before any new builder runs.
					for _, change := range []string{"skip_absent_workspace", "source_commit"} {
						saved := req.Input[change]
						req.Input[change] = "changed"
						_, err = engine.ReplayJobRun(testBackground(), jobworkflow.ReplayRunRequest{JobKey: key})
						require.ErrorIs(t, err, jobdb.ErrWorkflowNotDeterministic)
						require.Equal(t, stableBuilds, builds)
						if saved == nil {
							delete(req.Input, change)
						} else {
							req.Input[change] = saved
						}
					}
					require.NoError(t, probeReplay(t, rt, key, w))
					require.JSONEq(t, string(original), string(jobdb.NewTaskDataOrPanic(req).GetDataOrPanic()), "fresh construction must not mutate the request map")
				})
			}
		}
	}
}

type hookPreparedDecisionContext struct {
	workspaceReplayContext
	result func(string) map[string]any
}

func (c *hookPreparedDecisionContext) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	data, err := build(testBackground())
	if err != nil {
		return nil, err
	}
	var req workerops.ActivityInvocationRequest
	if err = json.Unmarshal(data.GetDataOrPanic(), &req); err != nil {
		return nil, err
	}
	phase, _ := req.Input["phase"].(string)
	head := req.GitTaskContext.ResolvedBaseHash
	result := c.result(phase)
	result["path"] = ".c2/hooks/check.yaml"
	result["candidate"] = head
	result["source"] = recipe.RecipeSourceSnapshot{ResolvedCommit: head, ResolvedSelector: "git+file:///source//.c2/hooks/check.yaml@" + head}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{OpOutput: result, GitResult: contextual.GitCommitContext{PersistHash: head, ParentHash: head}})
	if err != nil {
		return nil, err
	}
	return jobdb.NewTaskData(env)
}
func TestHookAbsentPreparedDecision(t *testing.T) {
	for _, mode := range []string{"new", "legacy", "false", "invalid-present", "invalid-pin", "phase-reset"} {
		t.Run(mode, func(t *testing.T) {
			job, git := GenerateTestContext()
			parent, err := template.NewRecipeResolutionContext(&git, map[string]any{"caller-only": "private"}, job)
			require.NoError(t, err)
			grant := permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"token"}}}
			parent.Permissions = permissions.State{Version: permissions.Version, Grant: &grant, Limit: &grant}
			id, err := hookpolicy.Identity(job.GitBase.BaseRepo)
			require.NoError(t, err)
			policy := &hookpolicy.Snapshot{Repository: id, Tenant: &hookpolicy.Document{Version: 1, Hooks: map[string]hookpolicy.Entry{".c2/hooks/check.yaml": {Perms: permissions.Spec{JobDB: &grant}, Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{"token": {Mode: "candidate"}}}}}}}
			ctx := &hookPreparedDecisionContext{result: func(phase string) map[string]any {
				result := map[string]any{}
				if phase == "pin" {
					if mode == "invalid-pin" {
						result["absent"] = true
						result["skip_absent_workspace"] = true
					}
					if mode == "phase-reset" {
						result["absent"] = true
					}
					return result
				}
				if mode == "phase-reset" {
					result["recipe_yaml"] = "id: present\nsequence: []\noutputs: {status: present}\n"
					return result
				}
				result["absent"] = true
				if mode == "new" || mode == "invalid-present" {
					result["skip_absent_workspace"] = true
				}
				if mode == "false" {
					result["skip_absent_workspace"] = false
				}
				if mode == "invalid-present" {
					result["absent"] = false
				}
				return result
			}}
			include := &recipe.NodeInclude{NodeMetadata: recipe.NodeMetadata{ID: "hook", Workspace: &recipe.WorkspaceSpec{Cell: "forbidden"}, Inputs: map[string]any{"candidate": "${{ context.git.hash }}"}, Vars: map[string]any{"here": "caller"}}, Include: recipe.IncludeData{Recipe: "cell://.c2/hooks/check.yaml", IfMissing: &recipe.IncludeFallback{Outputs: map[string]any{"status": "skipped"}}}}
			original, err := json.Marshal(include)
			require.NoError(t, err)
			wrapper, err := prepareHook(workflow.Context{JobContext: ctx, HookPolicy: policy}, parent, include)
			if mode == "invalid-pin" || mode == "invalid-present" {
				require.ErrorContains(t, err, "invalid absent hook workspace decision")
				return
			}
			require.NoError(t, err)
			seq := wrapper.NodeImpl.(*recipe.NodeSequence)
			require.Equal(t, mode == "new", seq.Workspace == nil)
			inner := seq.Sequence[0].NodeImpl.(*recipe.NodeSequence)
			if mode == "new" {
				require.Equal(t, map[string]any{"candidate": "${{ inputs[\"candidate\"] }}"}, inner.Inputs)
			} else {
				require.Empty(t, inner.Inputs)
			}
			if mode == "phase-reset" {
				require.Equal(t, "present", inner.Outputs["status"])
			} else {
				require.Empty(t, seq.Internal.HookAuthority.Grant.Secrets.Resolve, "absence must discard configured and caller authority")
			}
			require.Equal(t, include.Inputs, seq.Inputs)
			require.Equal(t, include.Vars, seq.Vars)
			after, err := json.Marshal(include)
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(after), "generated wrappers must not mutate the source include")
		})
	}
}

// Exercise the input boundary through the complete sequence and direct-state
// compiler paths, in addition to the compiler/worker matrix above.
type hookLoadBoundaryContext struct {
	jobworkflow.ExecutionContext
	legacy, yield, reject bool
	builds                *int
}

func (c hookLoadBoundaryContext) DoScope(site string, options jobworkflow.ScopeOptions, body func(jobworkflow.ExecutionContext) (jobdb.JobData, error)) (jobdb.JobData, error) {
	return c.ExecutionContext.DoScope(site, options, func(inner jobworkflow.ExecutionContext) (jobdb.JobData, error) {
		next := c
		next.ExecutionContext = inner
		return body(next)
	})
}
func (c hookLoadBoundaryContext) DoTask(call jobworkflow.TaskCall, build func(context.Context) (jobdb.TaskData, error)) (jobdb.TaskData, error) {
	if call.TaskType != hookResolveTaskType || !strings.HasSuffix(call.Site, ":load") {
		return c.ExecutionContext.DoTask(call, build)
	}
	return c.ExecutionContext.DoTask(call, func(ctx context.Context) (jobdb.TaskData, error) {
		(*c.builds)++
		if c.reject {
			return nil, fmt.Errorf("load builder must not replace retained input")
		}
		if c.yield {
			return nil, c.ExecutionContext.(interface {
				Yield(context.Context, jobworkflow.YieldRequest) error
			}).Yield(ctx, jobworkflow.YieldRequest{Reschedule: jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}})
		}
		input, err := build(ctx)
		if err != nil || !c.legacy {
			return input, err
		}
		var req workerops.ActivityInvocationRequest
		if err := json.Unmarshal(input.GetDataOrPanic(), &req); err != nil {
			return nil, err
		}
		delete(req.Input, "skip_absent_workspace")
		return jobdb.NewTaskData(req)
	})
}
func TestHookAbsentCompilerLoadFrontiers(t *testing.T) {
	for _, state := range []bool{false, true} {
		for _, frontier := range []string{"legacy-waiting", "new-waiting", "descriptor"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, frontier), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "A")
				workspaceTestWorker(t)
				rt := newTimeoutReproRuntime(t, "sqlite")
				text := absentWorkspaceRecipe(state, "workspace: {cell: test}", "status: skipped", "")
				key := submitGitLifecycle(t, rt, repo, text)
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: `:pin"`, includeBase: true}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				builds := 0
				upgrade := false
				base := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})
				worker := jobworkflow.ExecutionWorker{JobType: "recipe", Execute: func(ctx jobworkflow.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
					return base.Run(hookLoadBoundaryContext{ExecutionContext: ctx, legacy: !upgrade && frontier == "legacy-waiting", yield: !upgrade && frontier == "descriptor", reject: upgrade && frontier != "descriptor", builds: &builds}, in)
				}}
				// Pin is already complete, so omitting the resolve worker persists an
				// external load wait; the descriptor case yields before any input exists.
				run, err := jobworkflow.GetJobForRun(testBackground(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, WorkerID: "frontier", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, err := run.Run(nil)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
				require.Equal(t, 1, builds)
				upgrade = true
				before, err := rt.ListChapters(testBackground(), jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(worker).BuildEngine()
				require.NoError(t, err)
				_, err = engine.ReplayJobRun(testBackground(), jobworkflow.ReplayRunRequest{JobKey: key})
				var miss jobworkflow.ReplayCacheMissError
				require.ErrorAs(t, err, &miss)
				var input workerops.ActivityInvocationRequest
				require.NoError(t, json.Unmarshal(miss.Input.GetDataOrPanic(), &input))
				require.Equal(t, frontier != "legacy-waiting", input.Input["skip_absent_workspace"] == true)
				wantBuilds := 1
				if frontier == "descriptor" {
					wantBuilds = 2
				}
				require.Equal(t, wantBuilds, builds)
				after, err := rt.ListChapters(testBackground(), jobdb.ListChaptersRequest{JobKey: key})
				require.NoError(t, err)
				require.Equal(t, before, after)
				// Resume with real guarded workers and the builder trap still installed.
				ws := workspaceTestWorker(t)
				guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
				var tasks []jobworkflow.TaskWorker
				for _, task := range ws.TaskWorkers {
					tasks = append(tasks, guard.WrapTaskWorker(task))
				}
				run, err = jobworkflow.GetJobForRun(testBackground(), guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: tasks, WorkerID: "resume", LeaseDuration: time.Minute})
				require.NoError(t, err)
				out, err = run.Run(nil)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.JSONEq(t, `{"result":{"status":"skipped"},"continued":true}`, string(out.Output.GetDataOrPanic()))
				if frontier == "descriptor" {
					wantBuilds++
				}
				require.Equal(t, wantBuilds, builds)
				history, err := jobdb.GetJobRun(testBackground(), rt, jobdb.GetJobRunRequest{JobKey: key})
				require.NoError(t, err)
				workspaceTasks := 0
				for _, task := range executionTaskRuns(history) {
					if task.TaskType == WorkspaceResolutionTaskType {
						workspaceTasks++
					}
				}
				wantWorkspace := 0
				if frontier == "legacy-waiting" {
					wantWorkspace = 1
				}
				require.Equal(t, wantWorkspace, workspaceTasks)
			})
		}
	}
}
