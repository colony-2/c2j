package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

type waitJobTool struct{ jt }

func (*waitJobTool) TaskOrdinal() int64 { return 1 }

type waitController struct {
	workspaceController
	persists int
}

func (c *waitController) PersistWithDiffs(ctx context.Context, g *gitstate.GitTaskContext) (*gitcommit.PersistWithDiffsOutput, []jobdb.Artifact, error) {
	c.persists++
	return c.workspaceController.PersistWithDiffs(ctx, g)
}
func TestExtensionWaitWorkspaceDiscard(t *testing.T) {
	for _, failCleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "W14_W15_W18_discard", true: "W19_cleanup_failure"}[failCleanup], func(t *testing.T) {
			repo, hash, cleanup := setupGitRepo(t)
			defer cleanup()
			var dir string
			jobContext := celltest.Context(context.Background())
			externalJobs := toy.New()
			celltest.Create(t, externalJobs, "test", "test", "example.test/test")
			var submitted jobdb.JobHandle
			external := filepath.Join(t.TempDir(), "external-cache")
			reg := ActivityRegistration{Step: recipeops.TaskStep{Invoke: func(deps recipeops.OpDependencies, ctx context.Context, _ map[string]any) (map[string]any, error) {
				require.Equal(t, "1", deps.ProtectedEnv()[extensionwait.CapabilityEnv])
				root := deps.WorktreePath()
				require.NoError(t, os.WriteFile(external, []byte("independent-cache"), 0600))
				var submitErr error
				submitted, submitErr = externalJobs.SubmitJob(jobContext, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: "external", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
				require.NoError(t, submitErr)
				_, objectErr := deps.Objects().Publish(ctx, "wait-test/v1", map[string]any{"discard": "object-marker"}, nil)
				require.NoError(t, objectErr)
				dir = filepath.Dir(root)
				require.NoError(t, os.WriteFile(filepath.Join(root, "committed"), []byte("discard-marker"), 0600))
				_, e := common.ExecuteGitCommand(ctx, root, "add", "committed")
				require.NoError(t, e)
				_, e = common.ExecuteGitCommand(ctx, root, "-c", "user.name=Wait", "-c", "user.email=wait@example.test", "commit", "-m", "discard-marker")
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(filepath.Join(root, "committed"), []byte("dirty"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(root, "staged"), []byte("dirty"), 0600))
				_, e = common.ExecuteGitCommand(ctx, root, "add", "staged")
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(filepath.Join(root, "untracked"), []byte("dirty"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored\n"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(root, "ignored"), []byte("dirty"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "outbox", "evidence"), []byte("discard-marker"), 0600))
				require.NoError(t, deps.AddOutputArtifact(jobdb.NewArtifactFromBytes("discard-marker", []byte("discard-marker"))))
				until := time.Now().UTC().Add(time.Hour)
				return map[string]any{"discard-marker": true}, extensionwait.New(extensionwait.Request{Version: 1, Until: &until})
			}}}
			ctl := &waitController{workspaceController: gitstate.NewController(nil)}
			executor := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: ctl}
			if failCleanup {
				executor.removeWaitDir = func(string) error { return errors.New("injected cleanup failure") }
			}
			var retained []func()
			ctx := dispatchtest.WithCleanupRetainer(context.Background(), func(f func()) { retained = append(retained, f) })
			out, arts, err := executor.do(ctx, &waitJobTool{jt{jobdb.JobKey{TenantId: "test", JobId: "wait"}}}, ActivityInvocationRequest{Input: map[string]any{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: hash}}, nil)
			require.True(t, extensionwait.IsDiscard(err))
			_, ready := extensionwait.Eligible(err)
			require.Equal(t, !failCleanup, ready)
			require.Equal(t, ActivityInvocationOutput{}, out)
			require.Empty(t, arts)
			require.Zero(t, ctl.persists)
			cache, readErr := os.ReadFile(external)
			require.NoError(t, readErr)
			require.Equal(t, "independent-cache", string(cache))
			_, readErr = externalJobs.GetJob(jobContext, submitted.JobKey)
			require.NoError(t, readErr, "external submissions survive the discarded attempt independently")
			if !failCleanup {
				_, e := os.Stat(dir)
				require.True(t, os.IsNotExist(e), "entire work directory must be removed despite retained test cleanup")
			}
			require.NoError(t, os.RemoveAll(dir))
			for _, f := range retained {
				f()
			}
		})
	}
}
