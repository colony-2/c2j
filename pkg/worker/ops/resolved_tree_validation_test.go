package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// A sentinel in the temporary directory name survives path hydration and trips
// the worker's existing unresolved-path check. The non-const trap control proves
// that this boundary is observable, independently of native adoption's guard.
func TestResolvedTreeWorkerRejectsConstBeforeHydration(t *testing.T) {
	for _, op := range []coreops.RegisterableOp{thinpackrebase.GetRebaseOp(), thinpackrebase.GetOp()} {
		for _, mode := range []string{"const-adoption-trap", "adoption-trap", "const-adoption", "adoption", "const-replay", "replay", "other-op"} {
			t.Run(op.GetMetadata().Type+"/"+mode, func(t *testing.T) {
				repo, base, cleanup := setupGitRepo(t)
				defer cleanup()
				ctx := celltest.Context(context.Background())
				tree, err := common.GetCommitHash(ctx, repo, "HEAD^{tree}")
				require.NoError(t, err)
				for hash, kind := range map[string]string{base: "commit", tree: "tree"} {
					require.Len(t, hash, 40)
					actual, err := common.ExecuteGitCommand(ctx, repo, "cat-file", "-t", hash)
					require.NoError(t, err)
					require.Equal(t, kind, strings.TrimSpace(string(actual)))
				}
				if strings.HasSuffix(mode, "-trap") {
					tmp := filepath.Join(t.TempDir(), contextual.OpWorktreePathSentinel)
					require.NoError(t, os.Mkdir(tmp, 0o755))
					t.Setenv("TMPDIR", tmp)
				}
				input := map[string]any{"repo_path": contextual.WorktreePathSentinel, "base_hash": base, "persist_hash": base,
					"target_base_hash": base, "resolution": map[string]any{"tree_hash": tree}}
				if strings.HasSuffix(mode, "replay") {
					delete(input, "resolution")
				}
				step := op.TaskChain()[0]
				native, invoked := step.Invoke, false
				step.Invoke = func(d coreops.OpDependencies, ctx context.Context, input map[string]any) (map[string]any, error) {
					invoked = true
					return native(d, ctx, input)
				}
				reg := ActivityRegistration{Activity: op, Step: step, Metadata: op.GetMetadata()}
				if mode == "other-op" {
					reg.Metadata.Type = "unrelated"
					reg.Step.Invoke = func(_ coreops.OpDependencies, _ context.Context, input map[string]any) (map[string]any, error) {
						invoked = true
						return map[string]any{"new_persist_hash": base}, nil
					}
				}
				executor := opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), reg: reg, controller: gitstate.NewController(nil)}
				out, artifacts, err := executor.do(ctx, &jt{jobdb.JobKey{TenantId: "test", JobId: "const-adoption"}}, ActivityInvocationRequest{
					Const: strings.HasPrefix(mode, "const-") || mode == "other-op", Input: input,
					GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: base, ResolvedBaseHash: base, PersistHash: base, ParentHash: base, CellName: "cells/test"},
				}, nil)
				switch mode {
				case "const-adoption-trap", "const-adoption", "adoption-trap":
					if mode != "adoption-trap" {
						require.ErrorContains(t, err, "resolved-tree adoption requires workspace persistence; const operations discard changes")
					} else {
						require.ErrorContains(t, err, "op-visible path resolution failed")
					}
					require.False(t, invoked)
					require.Empty(t, out.OpOutput)
					require.Empty(t, out.GitResult)
					require.Empty(t, artifacts)
				default:
					require.NoError(t, err)
					require.True(t, invoked)
					require.Equal(t, base, out.OpOutput["new_persist_hash"])
					require.Equal(t, base, out.GitResult.PersistHash)
				}
				require.Equal(t, base, out.WorkspaceBefore.BaseCommit)
				require.Equal(t, base, out.WorkspaceBefore.HeadCommit)
				if out.WorkspaceAfter != nil {
					require.Equal(t, *out.WorkspaceBefore, *out.WorkspaceAfter)
				}
				head, err := common.GetCommitHash(ctx, repo, "HEAD")
				require.NoError(t, err)
				require.Equal(t, base, head)
				status, err := common.ExecuteGitCommand(ctx, repo, "status", "--porcelain")
				require.NoError(t, err)
				require.Empty(t, status)
			})
		}
	}
}

// Bypass the compiler entirely. A worker receiving a retained/older invocation
// must reject default-derived or missing adoption confirmations independently.
func TestResolvedTreeWorkerRejectsDefaultsBeforeHydration(t *testing.T) {
	for _, op := range []coreops.RegisterableOp{thinpackrebase.GetRebaseOp(), thinpackrebase.GetOp()} {
		for _, mode := range []string{"task-defaults", "missing-base", "missing-persist", "null", "replay"} {
			t.Run(op.GetMetadata().Type+"/"+mode, func(t *testing.T) {
				repo, base, cleanup := setupGitRepo(t)
				defer cleanup()
				tree, err := common.GetCommitHash(context.Background(), repo, "HEAD^{tree}")
				require.NoError(t, err)
				input := map[string]any{"repo_path": contextual.WorktreePathSentinel, "base_hash": base, "persist_hash": base,
					"target_base_hash": base, "resolution": map[string]any{"tree_hash": tree}}
				switch mode {
				case "task-defaults", "replay":
					input["base_hash"], input["persist_hash"] = contextual.GitExecutionBaseSentinel, contextual.GitExecutionHeadSentinel
					if mode == "replay" {
						delete(input, "resolution")
					}
				case "missing-base":
					delete(input, "base_hash")
				case "missing-persist":
					delete(input, "persist_hash")
				case "null":
					input["resolution"] = nil
				}
				step := op.TaskChain()[0]
				native, invoked := step.Invoke, false
				step.Invoke = func(d coreops.OpDependencies, ctx context.Context, input map[string]any) (map[string]any, error) {
					invoked = true
					return native(d, ctx, input)
				}
				reg := ActivityRegistration{Activity: op, Step: step, Metadata: op.GetMetadata()}
				executor := opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), reg: reg, controller: gitstate.NewController(nil)}
				out, _, err := executor.do(celltest.Context(context.Background()), &jt{jobdb.JobKey{TenantId: "test", JobId: "adoption-inputs"}}, ActivityInvocationRequest{
					Input:          input,
					GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: base, ResolvedBaseHash: base, PersistHash: base, ParentHash: base, CellName: "cells/test"},
				}, nil)
				if mode == "replay" {
					require.NoError(t, err)
					require.True(t, invoked)
					require.Equal(t, base, out.OpOutput["new_persist_hash"])
				} else {
					require.Error(t, err)
					require.False(t, invoked, "invalid confirmations must fail before native mutation")
					require.Empty(t, out.GitResult)
				}
			})
		}
	}
}
