package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRebaseSharedHistoryPersistenceAndReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, selector := range []string{"rebase", "thinpackrebase"} {
			for _, pending := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/pending=%t", backend, selector, pending), func(t *testing.T) {
					repo := filepath.Join(t.TempDir(), "source")
					base := workspaceRepo(t, repo, "old base")
					require.NoError(t, runGit(repo, "git", "checkout", "-b", "upstream"))
					require.NoError(t, os.WriteFile(filepath.Join(repo, "identity"), []byte("shared upstream"), 0644))
					shared := resolvedCommit(t, repo, "shared upstream")
					require.NoError(t, os.WriteFile(filepath.Join(repo, "identity"), []byte("pinned target"), 0644))
					target := resolvedCommit(t, repo, "pinned target")
					require.NoError(t, runGit(repo, "git", "checkout", "main"))
					prepare := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "shared_prepare"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
						_, err := common.ExecuteGitCommand(ctx, d.WorktreePath(), "fetch", "origin", shared)
						require.NoError(t, err)
						_, err = common.ExecuteGitCommand(ctx, d.WorktreePath(), "reset", "--hard", shared)
						require.NoError(t, err)
						if pending {
							require.NoError(t, os.WriteFile(filepath.Join(d.WorktreePath(), "local"), []byte("local work"), 0644))
							resolvedCommit(t, d.WorktreePath(), "local work")
						}
						before := resolvedObserve(t, d, ctx)
						require.Equal(t, base, before["base"], "the persisted base still predates shared upstream")
						return before, nil
					})
					observe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "shared_observe"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
						return resolvedObserve(t, d, ctx), nil
					})
					calls, replayDir := 0, ""
					op := thinpackrebase.GetRebaseOp()
					if selector == "thinpackrebase" {
						op = thinpackrebase.GetOp()
					}
					counted := resolvedCountedOp{op, func(d coreops.OpDependencies, ctx context.Context, in map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
						calls++
						replayDir = d.WorktreePath()
						require.NotContains(t, in, "resolution")
						require.Equal(t, base, in["base_hash"])
						return native(d, ctx, in)
					}}
					workspaceTestWorker(t, prepare, observe, counted)
					yaml := fmt.Sprintf(`id: shared-history
sequence:
 - id: prepare
   op: shared_prepare
 - id: integrate
   op: %s
   inputs: {target_base_hash: %s}
 - id: observe
   op: shared_observe
outputs:
 prepared: '${{ sequence.prepare.outputs }}'
 integration: '${{ sequence.integrate.outputs }}'
 restored: '${{ sequence.observe.outputs }}'
`, selector, target)
					rt := newTimeoutReproRuntime(t, backend)
					key := submitGitLifecycle(t, rt, repo, yaml)
					fault := &gitOutcomeInterruption{ConsumerRuntime: rt, match: `task:"` + selector + `:` + selector + `"`}
					_, err := runGitLifecycle(t, fault, key)
					require.Error(t, err)
					require.True(t, fault.fired)
					require.Equal(t, 1, calls)
					require.NoError(t, os.RemoveAll(replayDir))
					// A replacement must use the recorded target even after main moves.
					require.NoError(t, runGit(repo, "git", "reset", "--hard", target))
					require.NoError(t, os.WriteFile(filepath.Join(repo, "unconfirmed"), []byte("newer"), 0644))
					resolvedCommit(t, repo, "advance after replay")
					out, err := runGitLifecycle(t, rt, key)
					require.NoError(t, err)
					result := gitLifecycleOutput(t, out)
					prepared := result["prepared"].(map[string]any)
					integration := result["integration"].(map[string]any)
					restored := result["restored"].(map[string]any)
					require.Equal(t, target, restored["base"])
					require.Equal(t, target, integration["new_base_hash"])
					require.Equal(t, target, integration["target_base_hash"])
					require.Equal(t, base, integration["rebased_from"].(map[string]any)["base_hash"])
					require.Equal(t, prepared["head"], integration["rebased_from"].(map[string]any)["persist_hash"])
					require.Equal(t, integration["new_persist_hash"], restored["head"])
					require.NotEqual(t, replayDir, restored["dir"])
					require.NotContains(t, restored["diff"], "identity", "replacement snapshot contains only the remaining local delta")
					if pending {
						require.Equal(t, target, restored["parents"])
						require.Contains(t, restored["diff"], "+local work")
					} else {
						require.Equal(t, target, restored["head"])
						require.Nil(t, restored["restore"], "discard the obsolete bundle when all commits are upstream")
					}
					assertResolvedArtifacts(t, rt, key, selector, !pending, target, restored["head"].(string), restored["diff"].(string))
					engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
					require.NoError(t, err)
					require.NoError(t, os.Rename(repo, repo+"-offline"))
					played, err := engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
					require.NoError(t, err)
					require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(played.GetDataOrPanic()))
					require.Equal(t, 1, calls)
					t.Logf("base=%s shared=%s target=%s candidate=%v result=%v tree=%v", base, shared, target, prepared["head"], restored["head"], restored["tree"])
				})
			}
		}
	}
}
