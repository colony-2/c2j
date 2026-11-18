package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestThinpackRefreshDefaultsPersistenceAndReplay(t *testing.T) {
	for _, tc := range []struct{ pending, included, scoped bool }{{}, {pending: true}, {pending: true, included: true}, {pending: true, scoped: true}, {scoped: true}} {
		t.Run(fmt.Sprintf("pending_%t_included_%t_scoped_%t", tc.pending, tc.included, tc.scoped), func(t *testing.T) {
			pending := tc.pending
			ctx := celltest.Context(context.Background())
			repo := filepath.Join(t.TempDir(), "repo")
			initial := workspaceRepo(t, repo, "root")
			type probeInput struct {
				Base  string `json:"base" default:"{{ context.git.execution.base_hash }}" validate:"required,hexadecimal,len=40"`
				Head  string `json:"head" default:"{{ context.git.execution.head_hash }}" validate:"required,hexadecimal,len=40"`
				Early string `json:"early" default:"{{ context.git.resolved_hash }}"`
			}
			calls := map[string]int{}
			target := ""
			advance := func(name string) (string, error) {
				if e := os.WriteFile(filepath.Join(repo, name), []byte(name), 0644); e != nil {
					return "", e
				}
				if e := runGit(repo, "git", "add", "."); e != nil {
					return "", e
				}
				if e := runGit(repo, "git", "commit", "-m", name); e != nil {
					return "", e
				}
				return common.GetCommitHash(ctx, repo, "HEAD")
			}

			makeProbe := func(name string) coreops.RegisterableOp {
				return coreops.NewActivityMappedOpV2[probeInput, map[string]any](coreops.OpMetadata{Type: name}, func(deps coreops.OpDependencies, ctx context.Context, in probeInput) (map[string]any, error) {
					calls[name]++
					actual, e := common.GetCommitHash(ctx, deps.WorktreePath(), "HEAD")
					if e != nil {
						return nil, e
					}
					if in.Head != actual || in.Base != deps.GitContext().ResolvedBaseHash {
						return nil, fmt.Errorf("execution defaults do not match restored workspace")
					}
					switch name {
					case "refresh_seed":
						if in.Early != "" {
							return nil, fmt.Errorf("job base pinned before mutation")
						}
						if pending {
							if e = os.WriteFile(filepath.Join(deps.WorktreePath(), "pending"), []byte("local"), 0644); e != nil {
								return nil, e
							}
						}
					case "refresh_advance":
						if tc.included {
							if e = os.WriteFile(filepath.Join(repo, "pending"), []byte("local"), 0644); e != nil {
								return nil, e
							}
						}
						target, e = advance("child-change")
						if e != nil {
							return nil, e
						}
					case "refresh_after":
						if in.Base != target {
							return nil, fmt.Errorf("lost refreshed base: %s vs %s", in.Base, target)
						}
						if _, e = os.Stat(filepath.Join(deps.WorktreePath(), "child-change")); e != nil {
							return nil, e
						}
						if _, e = os.Stat(filepath.Join(deps.WorktreePath(), "later")); !os.IsNotExist(e) {
							return nil, fmt.Errorf("refresh did not pin completed base")
						}
						if pending {
							b, e := os.ReadFile(filepath.Join(deps.WorktreePath(), "pending"))
							if e != nil {
								return nil, e
							}
							if string(b) != "local" {
								return nil, fmt.Errorf("lost pending work")
							}
						}
					}
					return map[string]any{"base": in.Base, "head": in.Head, "target": target}, nil
				})
			}
			withRegisteredOps(t, thinpackrebase.GetOp(), makeProbe("refresh_seed"), makeProbe("refresh_advance"), makeProbe("refresh_after"))
			registry, e := workerops.NewActivityRegistry()
			require.NoError(t, e)
			refreshCalls := 0
			for name, reg := range registry.GetAll() {
				if reg.Metadata.Type == "thinpackrebase" {
					invoke := reg.Step.Invoke
					reg.Step.Invoke = func(d coreops.OpDependencies, c context.Context, i map[string]any) (map[string]any, error) {
						refreshCalls++
						return invoke(d, c, i)
					}
					registry.UpdateRegistration(name, reg)
				}
			}
			ws, e := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
			require.NoError(t, e)
			rt := toyruntime.New()
			guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
			eng, e := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("refresh").PlusWorkers(ws.JobWorker).BuildEngine()
			require.NoError(t, e)
			eng = jobdbschema.WorkflowEngine{Engine: eng, Registry: rt}
			rec, e := recipe.LoadRecipeFromString([]byte(`id: refresh
sequence:
 - id: seed
   op: refresh_seed
 - id: advance
   op: refresh_advance
 - id: refresh
   op: thinpackrebase
   inputs:
     target_base_hash: '${{ sequence.advance.outputs.target }}'
 - id: after
   op: refresh_after
outputs:
 refresh: '${{ sequence.refresh.outputs }}'
 after: '${{ sequence.after.outputs }}'
`))
			require.NoError(t, e)
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
			if tc.scoped {
				job.Workspace = &contextual.WorkspaceContext{Cell: "root", ScopeID: "refresh-scope", InitialHash: initial}
			}
			celltest.Create(t, rt, "refresh", "test", celltest.Repo(job.GitBase.BaseRepo))
			keyRef, e := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "refresh", RecipeName: "refresh", GitRef: "main", JobContext: job, Cell: "test", CellRepo: celltest.Repo(job.GitBase.BaseRepo)}, eng, *rec)
			key := keyRef.Key()
			require.NoError(t, e)
			var first, all []jobworkflow.TaskWorker
			for name, w := range ws.TaskWorkers {
				w = guard.WrapTaskWorker(w)
				all = append(all, w)
				if name != "refresh_after:refresh_after" {
					first = append(first, w)
				}
			}
			run := func(workers []jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
				h, e := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, e)
				out, e := h.Run(nil)
				require.NoError(t, e)
				return out
			}
			firstRun := run(first)
			require.Equal(t, jobworkflow.JobRunSuspended, firstRun.Status, "%v", firstRun.JobError)
			require.NotEqual(t, initial, target)
			require.Equal(t, 1, refreshCalls)
			_, e = advance("later")
			require.NoError(t, e)
			done := run(all)
			require.Equal(t, jobworkflow.JobRunCompleted, done.Status, "%v", done.JobError)
			require.Equal(t, map[string]int{"refresh_seed": 1, "refresh_advance": 1, "refresh_after": 1}, calls)
			require.Equal(t, 1, refreshCalls)
			result, e := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, e)
			raw, e := result.GetData()
			require.NoError(t, e)
			var values map[string]map[string]any
			require.NoError(t, json.Unmarshal(raw, &values))
			require.Equal(t, target, values["refresh"]["new_base_hash"])
			require.Equal(t, values["refresh"]["new_persist_hash"], values["after"]["head"])
			// Completed replay must not inspect Git or rerun the operation.
			require.NoError(t, os.RemoveAll(repo))
			replay, e := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, e)
			replayRaw, e := replay.GetData()
			require.NoError(t, e)
			require.JSONEq(t, string(raw), string(replayRaw))
			require.Equal(t, 1, refreshCalls)
		})
	}
}

// Keep published upstream commits inside the candidate while its managed base
// remains old. The two upstream edits deliberately conflict if replayed again.
func replayFixture(t *testing.T, mode string) (repo, base, incorporated, target string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "upstream")
	base = workspaceRepo(t, repo, "base")
	if mode == "normal" || mode == "fast-forward" {
		require.NoError(t, runGit(repo, "git", "checkout", "-b", "target"))
		require.NoError(t, os.WriteFile(filepath.Join(repo, "later-upstream"), []byte("later\n"), 0644))
		target = resolvedCommit(t, repo, "upstream")
		incorporated = base
	} else {
		require.NoError(t, runGit(repo, "git", "checkout", "-b", "target"))
		for _, content := range []string{"first upstream\n", "second upstream\n"} {
			require.NoError(t, os.WriteFile(filepath.Join(repo, "identity"), []byte(content), 0644))
			incorporated = resolvedCommit(t, repo, content)
		}
		target = incorporated
		if mode == "later" || mode == "conflict" {
			name, content := "later-upstream", "later\n"
			if mode == "conflict" {
				name, content = "identity", "conflicting upstream\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(content), 0644))
			target = resolvedCommit(t, repo, "later upstream")
		}
	}
	require.NoError(t, runGit(repo, "git", "checkout", "main"))
	return
}

func replayFixtureOps(t *testing.T, mode, incorporated string) []coreops.RegisterableOp {
	t.Helper()
	var retained string
	prepare := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "replay_prepare"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		repo := d.WorktreePath()
		if mode != "normal" && mode != "fast-forward" {
			_, err := common.ExecuteGitCommand(ctx, repo, "fetch", "--depth", "8", "origin", incorporated)
			require.NoError(t, err)
			require.NoError(t, runGit(repo, "git", "reset", "--hard", incorporated))
		}
		retained = resolvedHash(t, repo, "HEAD")
		if mode != "fast-forward" {
			name, content := "feature", "feature\n"
			if mode == "conflict" {
				name, content = "identity", "conflicting pending work\n"
			}
			require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(content), 0644))
			retained = resolvedCommit(t, repo, "retained feature")
			require.NoError(t, os.WriteFile(filepath.Join(repo, "temporary-hook"), []byte("hook\n"), 0644))
			resolvedCommit(t, repo, "sole temporary hook")
		}
		result := resolvedObserve(t, d, ctx)
		result["retained"] = retained
		if mode == "equal-candidate" {
			require.NoError(t, runGit(repo, "git", "push", "origin", "HEAD:refs/heads/prepared"))
		}
		return result, nil
	})
	observe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "replay_observe"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		result := resolvedObserve(t, d, ctx)
		result["parent_diff"] = ""
		if mode != "equal-candidate" && mode != "fast-forward" {
			result["parent_diff"] = string(mustReplayGit(t, d.WorktreePath(), "diff", "--unified=10", "HEAD^", "HEAD"))
		}
		result["pending_count"] = strings.TrimSpace(string(mustReplayGit(t, d.WorktreePath(), "rev-list", "--count", d.GitContext().ResolvedBaseHash+"..HEAD")))
		if mode == "ancestor" {
			result["guard_count"] = strings.TrimSpace(string(mustReplayGit(t, d.WorktreePath(), "rev-list", "--count", retained+"..HEAD")))
			result["guard_files"] = strings.TrimSpace(string(mustReplayGit(t, d.WorktreePath(), "diff", "--name-only", retained, "HEAD")))
		}
		for _, name := range []string{"identity", "feature", "temporary-hook", "later-upstream", "unconfirmed-newer"} {
			data, err := os.ReadFile(filepath.Join(d.WorktreePath(), name))
			if !os.IsNotExist(err) {
				require.NoError(t, err)
				result[name] = string(data)
			}
		}
		return result, nil
	})
	return []coreops.RegisterableOp{prepare, observe}
}

func replayRecipe(selector, target string, caught bool) string {
	body := fmt.Sprintf(`id: pending-replay
version: "1"
sequence:
 - id: prepare
   op: replay_prepare
 - id: integrate
   op: %s
   inputs:
    target_base_hash: %s
 - id: observe
   op: replay_observe
outputs:
 prepared: '${{ sequence.prepare.outputs }}'
 integration: '${{ sequence.integrate.outputs }}'
 restored: '${{ sequence.observe.outputs }}'
`, selector, target)
	if caught {
		body = strings.Replace(body, " - id: observe", `   catch:
    - when: true
      continue:
       outputs: {failure: '${{ failure }}'}
 - id: observe`, 1)
	}
	return body
}

func replayOp(selector string) coreops.RegisterableOp {
	if selector == "rebase" {
		return thinpackrebase.GetRebaseOp()
	}
	return thinpackrebase.GetOp()
}

func TestReplaySelectionFreshWorkerPersistenceAndOfflineReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, selector := range []string{"rebase", "thinpackrebase"} {
			for _, mode := range []string{"ancestor", "later", "equal-candidate", "normal", "fast-forward", "conflict"} {
				t.Run(backend+"/"+selector+"/"+mode, func(t *testing.T) {
					repo, base, incorporated, target := replayFixture(t, mode)
					calls := 0
					var operationDir string
					op := resolvedCountedOp{replayOp(selector), func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
						calls++
						operationDir = d.WorktreePath()
						return native(d, ctx, input)
					}}
					workspaceTestWorker(t, append(replayFixtureOps(t, mode, incorporated), op)...)
					rt := newTimeoutReproRuntime(t, backend)
					requested := target
					if mode == "equal-candidate" {
						requested = "'${{ sequence.prepare.outputs.head }}'"
					}
					key := submitGitLifecycle(t, rt, repo, replayRecipe(selector, requested, mode == "conflict"))
					fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: `task:"` + selector + `:` + selector + `"`}
					_, err := runGitLifecycle(t, fault, key)
					require.Error(t, err)
					require.True(t, fault.fired)
					require.Equal(t, 1, calls)
					require.NoError(t, os.RemoveAll(operationDir))
					require.NoError(t, runGit(repo, "git", "reset", "--hard", target))
					require.NoError(t, os.WriteFile(filepath.Join(repo, "unconfirmed-newer"), []byte("unconfirmed"), 0644))
					resolvedCommit(t, repo, "advance main after completed operation")
					out, err := runGitLifecycle(t, rt, key)
					require.NoError(t, err)
					result := gitLifecycleOutput(t, out)
					prepared := result["prepared"].(map[string]any)
					integration := result["integration"].(map[string]any)
					restored := result["restored"].(map[string]any)
					require.Equal(t, 1, calls)
					require.NotEqual(t, operationDir, restored["dir"])
					require.Nil(t, restored["unconfirmed-newer"])
					if mode == "equal-candidate" {
						target = prepared["head"].(string)
					}
					if mode == "conflict" {
						failure := integration["failure"].(map[string]any)
						require.Equal(t, thinpackrebase.ConflictCode, failure["code"])
						details := failure["attrs"].(map[string]any)["rebase"].(map[string]any)
						require.Equal(t, true, details["workspace_restored"])
						require.NotEmpty(t, details["diagnostics"])
						require.Equal(t, base, restored["base"])
						require.Equal(t, prepared["head"], restored["head"])
						require.Equal(t, prepared["tree"], restored["tree"])
					} else {
						require.Equal(t, target, integration["new_base_hash"])
						require.Equal(t, target, restored["base"])
						require.Equal(t, base, integration["rebased_from"].(map[string]any)["base_hash"])
						require.Equal(t, prepared["head"], integration["rebased_from"].(map[string]any)["persist_hash"])
						require.Equal(t, integration["new_persist_hash"], restored["head"])
						if mode == "ancestor" || mode == "equal-candidate" {
							require.Equal(t, prepared["head"], restored["head"])
							require.Equal(t, prepared["tree"], restored["tree"])
							if mode == "ancestor" {
								require.Equal(t, "1", restored["guard_count"])
								require.Equal(t, "temporary-hook", restored["guard_files"])
							}
						}
						if mode == "later" || mode == "normal" {
							require.Equal(t, "2", restored["pending_count"])

							require.Equal(t, "later\n", restored["later-upstream"])
						}
						if mode != "fast-forward" {
							require.Equal(t, "feature\n", restored["feature"])
							require.Equal(t, "hook\n", restored["temporary-hook"])
						}
						if mode == "ancestor" || mode == "later" || mode == "equal-candidate" {
							require.Equal(t, "second upstream\n", restored["identity"])
						}
						equal := mode == "fast-forward" || mode == "equal-candidate"
						if equal {
							require.Equal(t, target, restored["head"])
							require.Nil(t, restored["restore"])
						}
						assertResolvedArtifacts(t, rt, key, selector, equal, target, restored["head"].(string), restored["diff"].(string), restored["parent_diff"].(string))
					}
					engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
					require.NoError(t, err)
					require.NoError(t, os.Rename(repo, repo+"-offline"))
					played, err := engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
					require.NoError(t, err)
					require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(played.GetDataOrPanic()))
					require.Equal(t, 1, calls)
				})
			}
		}
	}
}

func mustReplayGit(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	data, err := common.ExecuteGitCommand(celltest.Context(context.Background()), repo, args...)
	require.NoError(t, err)
	return data
}

func TestReplaySelectionShallowMergeFailureRestoresPriorPair(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, selector := range []string{"rebase", "thinpackrebase"} {
			t.Run(backend+"/"+selector, func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "source")
				base := workspaceRepo(t, repo, "base")
				commit := func(name, content string) string {
					require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(content), 0644))
					return resolvedCommit(t, repo, name+content)
				}
				require.NoError(t, runGit(repo, "git", "checkout", "-b", "upstream"))
				commit("identity", "first\n")
				target := commit("identity", "second\n")
				mergeSide := commit("upstream-extra", "extra\n")
				require.NoError(t, runGit(repo, "git", "checkout", "-b", "candidate", base))
				feature := commit("feature", "feature\n")
				require.NoError(t, runGit(repo, "git", "merge", "--no-ff", "--no-commit", mergeSide))
				candidate := commit("merge-resolution", "keep incorporated resolution\n")
				require.NoError(t, runGit(repo, "git", "merge-base", "--is-ancestor", target, candidate))
				require.NoError(t, runGit(repo, "git", "checkout", "main"))
				prepare := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "replay_prepare"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
					mustReplayGit(t, d.WorktreePath(), "fetch", "--depth", "100", "origin", candidate)
					mustReplayGit(t, d.WorktreePath(), "reset", "--hard", candidate)
					return resolvedObserve(t, d, ctx), nil
				})
				observe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "replay_observe"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
					data, err := os.ReadFile(filepath.Join(d.WorktreePath(), "merge-resolution"))
					require.NoError(t, err)
					require.Equal(t, "keep incorporated resolution\n", string(data))
					return resolvedObserve(t, d, ctx), nil
				})
				calls := 0
				var operationDir string
				op := resolvedCountedOp{replayOp(selector), func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
					calls++
					operationDir = d.WorktreePath()
					// Restore a real partial merge graph immediately before the native
					// operation. The prior recorded candidate has complete persistence.
					mustReplayGit(t, operationDir, "fetch", "--depth", "2", "origin", candidate)
					for _, hash := range []string{feature, target} {
						mustReplayGit(t, operationDir, "fetch", "--depth", "100", "origin", hash)
					}
					_, err := common.ExecuteGitCommand(ctx, operationDir, "merge-base", "--is-ancestor", target, candidate)
					require.Error(t, err, "the second-parent target ancestry must be hidden")
					require.NoError(t, runGit(operationDir, "git", "merge-base", "--is-ancestor", base, candidate))
					before := resolvedHash(t, operationDir, "HEAD^{tree}")
					result, err := native(d, ctx, input)
					require.ErrorContains(t, err, "incomplete shallow history")
					require.Equal(t, candidate, resolvedHash(t, operationDir, "HEAD"))
					require.Equal(t, before, resolvedHash(t, operationDir, "HEAD^{tree}"))
					return result, err
				}}
				workspaceTestWorker(t, prepare, observe, op)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, replayRecipe(selector, target, true))
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: `task:"` + selector + `:` + selector + `"`}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				require.Equal(t, 1, calls)
				require.NoError(t, os.RemoveAll(operationDir))
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				result := gitLifecycleOutput(t, out)
				prepared := result["prepared"].(map[string]any)
				restored := result["restored"].(map[string]any)
				failure := result["integration"].(map[string]any)["failure"].(map[string]any)
				require.Equal(t, thinpackrebase.FailedCode, failure["code"])
				details := failure["attrs"].(map[string]any)["rebase"].(map[string]any)
				require.Equal(t, "ancestry", details["phase"])
				require.Equal(t, true, details["workspace_restored"])
				require.NotEmpty(t, details["diagnostics"])
				require.Equal(t, base, restored["base"])
				require.Equal(t, candidate, restored["head"])
				require.Equal(t, prepared["tree"], restored["tree"])
				require.NotEqual(t, operationDir, restored["dir"])
				require.Equal(t, 1, calls, "resumption must consume the recorded rejection")
			})
		}
	}
}

func TestReplaySelectionUnrecordedOutcomeRestoresPriorPair(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, selector := range []string{"rebase", "thinpackrebase"} {
			for _, mode := range []string{"ancestor", "later"} {
				for _, failure := range []string{"outcome", "artifact-upload"} {
					t.Run(backend+"/"+selector+"/"+mode+"/"+failure, func(t *testing.T) {
						repo, base, incorporated, target := replayFixture(t, mode)
						calls := 0
						var prior map[string]any
						var previousDir string
						op := resolvedCountedOp{replayOp(selector), func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
							calls++
							before := resolvedObserve(t, d, ctx)
							require.Equal(t, base, before["base"])
							if prior != nil {
								for _, field := range []string{"head", "tree", "base", "restore"} {
									require.Equal(t, prior[field], before[field], field)
								}
								require.NotEqual(t, previousDir, d.WorktreePath())
							}
							prior, previousDir = before, d.WorktreePath()
							return native(d, ctx, input)
						}}
						workspaceTestWorker(t, append(replayFixtureOps(t, mode, incorporated), op)...)
						rt := newTimeoutReproRuntime(t, backend)
						key := submitGitLifecycle(t, rt, repo, replayRecipe(selector, target, false))
						fault := &resolvedOutcomeFault{WorkflowRuntime: rt, selector: selector, artifact: failure == "artifact-upload"}
						_, err := runGitLifecycle(t, fault, key)
						require.Error(t, err)
						require.True(t, fault.fired)
						require.Equal(t, 1, calls)
						require.NoError(t, os.RemoveAll(previousDir))
						history, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
						require.NoError(t, err)
						for _, entry := range history.Execution {
							require.False(t, entry.Record.Kind == "result" && strings.Contains(entry.Record.ID, `task:"`+selector+`:`+selector+`"`), "failed recording must not accept a result")
						}
						out, err := runGitLifecycle(t, rt, key)
						require.NoError(t, err)
						result := gitLifecycleOutput(t, out)
						restored := result["restored"].(map[string]any)
						require.Equal(t, target, restored["base"])
						require.Equal(t, result["integration"].(map[string]any)["new_persist_hash"], restored["head"])
						if mode == "ancestor" {
							require.Equal(t, prior["tree"], restored["tree"])
							require.Equal(t, prior["head"], restored["head"])
						}
						require.Equal(t, 2, calls, "only the unaccepted operation is retried")
					})
				}
			}
		}
	}
}

func TestReplaySelectionPersistenceFailureRetainsPriorPair(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, selector := range []string{"rebase", "thinpackrebase"} {
			for _, mode := range []string{"ancestor", "later"} {
				for _, failure := range []string{"base-setter", "pack", "diff"} {
					t.Run(backend+"/"+selector+"/"+mode+"/"+failure, func(t *testing.T) {
						repo, base, incorporated, target := replayFixture(t, mode)
						realGit, err := exec.LookPath("git")
						require.NoError(t, err)
						marker, shim := filepath.Join(t.TempDir(), "worktree"), t.TempDir()
						script := `#!/bin/sh
if test -f "$C2J_REPLAY_MARKER" && test "$(cat "$C2J_REPLAY_MARKER")" = "$PWD"; then
 case "$C2J_REPLAY_FAILURE:$1:$2" in
  pack:bundle:create|diff:diff:--unified=10) echo "injected replay $C2J_REPLAY_FAILURE failure" >&2; exit 73;;
 esac
fi
exec "$C2J_REPLAY_GIT" "$@"
`
						require.NoError(t, os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0755))
						t.Setenv("C2J_REPLAY_MARKER", marker)
						t.Setenv("C2J_REPLAY_FAILURE", failure)
						t.Setenv("C2J_REPLAY_GIT", realGit)
						t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
						var prior map[string]any
						op := resolvedCountedOp{replayOp(selector), func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
							prior = resolvedObserve(t, d, ctx)
							if failure == "base-setter" {
								return native(struct{ coreops.OpDependencies }{d}, ctx, input)
							}
							result, err := native(d, ctx, input)
							if err == nil {
								require.NoError(t, os.WriteFile(marker, []byte(d.WorktreePath()), 0600))
							}
							return result, err
						}}
						workspaceTestWorker(t, append(replayFixtureOps(t, mode, incorporated), op)...)
						rt := newTimeoutReproRuntime(t, backend)
						key := submitGitLifecycle(t, rt, repo, replayRecipe(selector, target, true))
						out, err := runGitLifecycle(t, rt, key)
						require.NoError(t, err)
						result := gitLifecycleOutput(t, out)
						restored := result["restored"].(map[string]any)
						require.Equal(t, base, restored["base"])
						for _, field := range []string{"head", "tree", "restore"} {
							if field == "restore" {
								require.Equal(t, resolvedJSON(t, prior[field]), resolvedJSON(t, restored[field]), field)
							} else {
								require.Equal(t, prior[field], restored[field], field)
							}
						}
						require.NotEqual(t, prior["dir"], restored["dir"])
						failureResult := result["integration"].(map[string]any)["failure"].(map[string]any)
						require.NotEqual(t, thinpackrebase.ConflictCode, failureResult["code"])
						if failure != "base-setter" {
							require.Contains(t, resolvedJSON(t, failureResult), "injected replay "+failure+" failure")
						}
					})
				}
			}
		}
	}
}

func TestReplaySelectionScopedWorkspaceIsolation(t *testing.T) {
	for _, selector := range []string{"rebase", "thinpackrebase"} {
		for _, mode := range []string{"ancestor", "later"} {
			t.Run(selector+"/"+mode, func(t *testing.T) {
				repo, base, incorporated, target := replayFixture(t, mode)
				outer := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "replay_outer"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
					return resolvedObserve(t, d, ctx), nil
				})
				workspaceTestWorker(t, append(replayFixtureOps(t, mode, incorporated), replayOp(selector), outer)...)
				inner := strings.TrimPrefix(replayRecipe(selector, target, false), "id: pending-replay\nversion: \"1\"\n")
				body := fmt.Sprintf(`id: scoped-replay
sequence:
 - id: enclosing
   op: workspace_probe
   inputs: {write: enclosing}
 - id: before
   op: replay_outer
 - id: scoped
   workspace: {cell: test, ref: %s}
   %s
 - id: after
   op: replay_outer
outputs:
 before: '${{ sequence.before.outputs }}'
 scoped: '${{ sequence.scoped.outputs }}'
 after: '${{ sequence.after.outputs }}'
`, base, strings.ReplaceAll(strings.TrimSpace(inner), "\n", "\n   "))
				rt := newTimeoutReproRuntime(t, "sqlite")
				key := submitGitLifecycle(t, rt, repo, body)
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				result := gitLifecycleOutput(t, out)
				before, after := result["before"].(map[string]any), result["after"].(map[string]any)
				for _, field := range []string{"base", "head", "tree", "diff"} {
					require.Equal(t, before[field], after[field], field)
				}
				restored := result["scoped"].(map[string]any)["restored"].(map[string]any)
				require.Equal(t, target, restored["base"])
				require.NotEqual(t, after["head"], restored["head"])
				require.NotContains(t, restored["diff"], "enclosing")
			})
		}
	}
}

func TestReplaySelectionAlternateCheckoutDoesNotAdoptManagedBase(t *testing.T) {
	for _, selector := range []string{"rebase", "thinpackrebase"} {
		t.Run(selector, func(t *testing.T) {
			repo, base, incorporated, target := replayFixture(t, "later")
			alternate := filepath.Join(t.TempDir(), "alternate")
			require.NoError(t, runGit(filepath.Dir(alternate), "git", "clone", repo, alternate))
			require.NoError(t, runGit(alternate, "git", "checkout", "--detach", incorporated))
			require.NoError(t, os.WriteFile(filepath.Join(alternate, "feature"), []byte("feature\n"), 0644))
			candidate := resolvedCommit(t, alternate, "pending alternate work")
			observe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "replay_outer"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
				return resolvedObserve(t, d, ctx), nil
			})
			workspaceTestWorker(t, observe, replayOp(selector))
			body := fmt.Sprintf(`id: alternate-replay
sequence:
 - id: before
   op: replay_outer
 - id: integrate
   op: %s
   inputs: {repo_path: %s, base_hash: %s, persist_hash: %s, target_base_hash: %s}
 - id: after
   op: replay_outer
outputs:
 before: '${{ sequence.before.outputs }}'
 integration: '${{ sequence.integrate.outputs }}'
 after: '${{ sequence.after.outputs }}'
`, selector, alternate, base, candidate, target)
			rt := newTimeoutReproRuntime(t, "sqlite")
			key := submitGitLifecycle(t, rt, repo, body)
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			result := gitLifecycleOutput(t, out)
			before, after := result["before"].(map[string]any), result["after"].(map[string]any)
			for _, field := range []string{"base", "head", "tree", "diff"} {
				require.Equal(t, before[field], after[field], field)
			}
			require.Equal(t, base, after["base"])
			require.Equal(t, target, resolvedHash(t, alternate, "HEAD^"))
			require.Equal(t, resolvedHash(t, alternate, "HEAD"), result["integration"].(map[string]any)["new_persist_hash"])
		})
	}
}
