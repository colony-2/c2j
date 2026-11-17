package compiler

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type resolvedSuspensionRuntime struct{ jobdb.WorkflowRuntime }

func (resolvedSuspensionRuntime) skipGitTask(name string) bool {
	return name == "resolved_observe:resolved_observe"
}

func TestResolvedTreeSuspensionKeepsAcceptedPair(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, selector := range []string{"rebase", "thinpackrebase"} {
			t.Run(backend+"/"+selector, func(t *testing.T) {
				repo, _, target := resolvedFixture(t)
				calls := 0
				op := thinpackrebase.GetRebaseOp()
				if selector == "thinpackrebase" {
					op = thinpackrebase.GetOp()
				}
				counted := resolvedCountedOp{op, func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
					calls++
					return native(d, ctx, input)
				}}
				workspaceTestWorker(t, append(resolvedFixtureOps(t, target, false), counted)...)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, resolvedRecipe(selector, target, "direct"))
				suspended, err := runGitLifecycle(t, resolvedSuspensionRuntime{rt}, key)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunSuspended, suspended.Status)
				require.Equal(t, 1, calls)
				require.NoError(t, runGit(repo, "git", "reset", "--hard", target))
				require.NoError(t, os.WriteFile(filepath.Join(repo, "after-suspension"), []byte("new upstream"), 0644))
				resolvedCommit(t, repo, "advance during suspension")
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				result := gitLifecycleOutput(t, out)
				adopted := result["integration"].(map[string]any)
				restored := result["restored"].(map[string]any)
				require.Equal(t, target, restored["base"])
				require.Equal(t, adopted["new_persist_hash"], restored["head"])
				require.Equal(t, result["prepared"].(map[string]any)["tree"], restored["tree"])
				require.Equal(t, 1, calls)
			})
		}
	}
}
