package ops

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestResourceRetryGitChildFork(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("Git fork interposition requires a C compiler")
	}
	lib := filepath.Join(t.TempDir(), "git_fork_eagain.so")
	buildOutput, err := exec.Command(cc, "-shared", "-fPIC", "-Wall", "-Werror", "-o", lib, "testdata/git_fork_eagain.c").CombinedOutput()
	require.NoError(t, err, "%s", buildOutput)
	repo, hash, cleanup := setupGitRepo(t)
	defer cleanup()

	// Use an actual Git process, not a synthesized error or a rejected Go Start.
	t.Setenv("LD_PRELOAD", lib)
	t.Setenv("LC_ALL", "C")
	controller := &retryWorkspace{Controller: gitstate.NewController(nil)}
	reg := ActivityRegistration{TaskType: "resource.git_child_fork", Step: coreops.TaskStep{Invoke: func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error) {
		t.Fatal("the operation body must not run when clone fails")
		return nil, nil
	}}}
	worker := &taskWorker{name: reg.TaskType, reg: reg, doer: &opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}}
	tc := wf.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "resource-git-child-fork"}, 1, nil, nil, nil)
	input := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{Input: map[string]any{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: hash}})
	out, err := coretask.RunLocal(metricsruntime.WrapWorker(worker, coretask.OutputKindActivityInvocationOutput), deniedTaskContext(t, tc), tc, input)
	require.True(t, resourceretry.Is(err), "%v", err)
	require.Nil(t, out)
	require.False(t, controller.persisted)
	require.NotEmpty(t, controller.workdir)
	require.NoDirExists(t, controller.workdir, "cleanup must finish before durable backoff")
}
