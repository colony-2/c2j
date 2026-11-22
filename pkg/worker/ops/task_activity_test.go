package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type activityArtifact struct {
	jobdb.Artifact
	read func() ([]byte, error)
}

func (a activityArtifact) Bytes(context.Context) ([]byte, error) { return a.read() }

func TestHealthTaskActivityRestorationAndFailure(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "direct"}[direct], func(t *testing.T) {
			for _, bulk := range []bool{false, true} {
				t.Run(map[bool]string{false: "inline", true: "artifact"}[bulk], func(t *testing.T) {
					active, finished, reads := false, false, 0
					selector := ""
					name := "extension_execution:extension_execution"
					local := task.WithActivityReporter(context.Background(), func(kind string) (func(string), func()) {
						require.Equal(t, name, kind)
						require.False(t, active)
						active = true
						return func(s string) { require.True(t, active); selector = s }, func() { require.True(t, active); active = false; finished = true }
					})
					inv := ActivityInvocationRequest{Input: map[string]any{"selector": "./does-not-exist"}}
					if direct {
						inv.Preparation.Extension = &ExtensionResolutionRequest{Selector: "./does-not-exist"}
					}
					if bulk {
						inv.Input["large"] = strings.Repeat("x", 130*1024)
					}
					data, err := NewInvocationTaskData(inv)
					require.NoError(t, err)
					if bulk {
						raw, _ := data.GetData()
						arts, _ := data.GetArtifacts()
						require.Len(t, arts, 1)
						original := arts[0]
						data, err = jobdb.NewTaskData(raw, activityArtifact{Artifact: original, read: func() ([]byte, error) {
							require.True(t, active)
							require.Empty(t, selector)
							reads++
							return original.Bytes(context.Background())
						}})
						require.NoError(t, err)
					}
					worker := &taskWorker{name: name, reg: ActivityRegistration{Metadata: ops.OpMetadata{Type: extops.ExecutionOpType}}}
					tc := wf.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "activity"}, 1, nil, nil, nil)
					// Missing permission context deliberately fails after selector restoration,
					// before any extension download or execution.
					_, err = worker.RunTask(local, tc, data)
					require.ErrorContains(t, err, "WrapTaskWorker")
					require.Equal(t, "./does-not-exist", selector)
					require.True(t, finished)
					require.False(t, active)
					if bulk {
						require.Equal(t, 1, reads)
					}
				})
			}
		})
	}
}

func TestHealthTaskActivityIncludesInputFailure(t *testing.T) {
	sentinel := errors.New("artifact unavailable")
	for _, malformed := range []bool{false, true} {
		active, finished := false, false
		local := task.WithActivityReporter(context.Background(), func(kind string) (func(string), func()) {
			require.Equal(t, "builtin:step", kind)
			active = true
			return func(string) { t.Fatal("unexpected selector") }, func() { active = false; finished = true }
		})
		data := jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{InputArtifact: "input.json"})
		if malformed {
			data = &jobdb.SimpleTaskData{Data: []byte("{")}
		} else {
			data, _ = jobdb.NewTaskData(ActivityInvocationRequest{InputArtifact: "input.json"}, activityArtifact{Artifact: jobdb.NewArtifactFromBytes("input.json", nil), read: func() ([]byte, error) { require.True(t, active); return nil, sentinel }})
		}
		worker := &taskWorker{name: "builtin:step"}
		tc := wf.NewTaskContext(jobdb.JobKey{}, 1, nil, nil, nil)
		_, err := worker.RunTask(local, tc, data)
		require.Error(t, err)
		if !malformed {
			require.ErrorIs(t, err, sentinel)
		}
		require.True(t, finished)
		require.False(t, active)
	}
}

func TestHealthBuiltinActivityIncludesCleanup(t *testing.T) {
	base, hash, cleanup := setupGitRepo(t)
	defer cleanup()
	active, finished, persisted := false, false, false
	reg := ActivityRegistration{TaskType: "health_builtin:step", Metadata: ops.OpMetadata{Type: "health_builtin"}, Step: ops.TaskStep{
		Invoke: func(_ ops.OpDependencies, _ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			require.True(t, active)
			return map[string]interface{}{}, nil
		},
	}}
	controller := timedWorkspace{Controller: gitstate.NewController(nil), clock: &metricClock{}, persist: func(*gitstate.GitTaskContext) { require.True(t, active); persisted = true }}
	worker := &taskWorker{name: reg.TaskType, reg: reg, doer: &opExecutor{deps: ops.NewServiceDepsBuilder().Build(), reg: reg, controller: controller}}
	tc := wf.NewTaskContext(jobdb.JobKey{TenantId: "tenant", JobId: "job"}, 1, nil, nil, nil)
	local := task.WithActivityReporter(deniedTaskContext(t, tc), func(kind string) (func(string), func()) {
		require.Equal(t, reg.TaskType, kind)
		active = true
		return func(string) { t.Fatal("builtin published extension") }, func() { active = false; finished = true }
	})
	_, err := worker.RunTask(local, tc, jobdb.NewTaskDataOrPanic(ActivityInvocationRequest{Input: map[string]any{}, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: base, BaseRef: hash}}))
	require.NoError(t, err)
	require.True(t, persisted)
	require.True(t, finished)
	require.False(t, active)
}
