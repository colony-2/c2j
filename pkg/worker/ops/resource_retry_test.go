package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type retryWorkspace struct {
	*gitstate.Controller
	restoreErr error
	workdir    string
	persisted  bool
}

func (w *retryWorkspace) Restore(ctx context.Context, task *gitstate.GitTaskContext, artifact jobdb.Artifact) error {
	w.workdir = filepath.Dir(task.WorktreePath)
	if w.restoreErr != nil {
		return w.restoreErr
	}
	return w.Controller.Restore(ctx, task, artifact)
}
func (w *retryWorkspace) PersistWithDiffs(ctx context.Context, task *gitstate.GitTaskContext) (*gitcommit.PersistWithDiffsOutput, []jobdb.Artifact, error) {
	w.persisted = true
	return w.Controller.PersistWithDiffs(ctx, task)
}

func TestResourceRetryWorkspaceBoundaries(t *testing.T) {
	repo, hash, cleanup := setupGitRepo(t)
	defer cleanup()
	for _, mode := range []string{"spawn", "flattened upstream", "permission", "cancellation", "body failure", "extension control"} {
		t.Run(mode, func(t *testing.T) {
			controller := &retryWorkspace{Controller: gitstate.NewController(nil)}
			safe := mode == "spawn" || mode == "flattened upstream" || mode == "extension control"
			switch mode {
			case "spawn":
				controller.restoreErr = &os.PathError{Op: "fork/exec", Path: "/usr/bin/git", Err: syscall.EAGAIN}
			case "flattened upstream":
				controller.restoreErr = errors.New("clone workspace: failed to clone repository: fork/exec /usr/bin/git: resource temporarily unavailable (output: )")
			case "permission":
				controller.restoreErr = &os.PathError{Op: "fork/exec", Path: "git", Err: syscall.EACCES}
			case "cancellation":
				controller.restoreErr = context.Canceled
			}
			calls := 0
			reg := ActivityRegistration{TaskType: "resource.boundary", Step: coreops.TaskStep{Invoke: func(deps coreops.OpDependencies, ctx context.Context, input map[string]any) (map[string]any, error) {
				calls++
				if mode == "extension control" {
					paths := deps.(coreops.OperationPathProvider).OperationPaths()
					require.NoError(t, os.WriteFile(filepath.Join(paths.Outbox, "discard.txt"), []byte("private attempt"), 0600))
					require.NoError(t, os.WriteFile(filepath.Join(deps.WorktreePath(), "discard.txt"), []byte("private attempt"), 0600))
					return nil, resourceretry.Restart("local_spawn", "launch")
				}
				return nil, errors.New("fork/exec git: resource temporarily unavailable")
			}}}
			worker := &taskWorker{name: reg.TaskType, reg: reg, doer: &opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}}
			tc := wf.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "resource-boundary"}, 1, nil, nil, nil)
			input := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{Input: map[string]any{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: hash}})
			out, err := coretask.RunLocal(metricsruntime.WrapWorker(worker, coretask.OutputKindActivityInvocationOutput), deniedTaskContext(t, tc), tc, input)
			require.Error(t, err)
			require.Equal(t, safe, resourceretry.Is(err), "%v", err)
			if safe {
				require.Nil(t, out, "waiting must bypass failure metrics, artifacts and output envelopes")
				require.False(t, controller.persisted)
				require.NoDirExists(t, controller.workdir, "cleanup must finish before durable backoff")
			}
			if controller.restoreErr != nil {
				require.Zero(t, calls)
			} else {
				require.Equal(t, 1, calls)
			}
		})
	}
}
