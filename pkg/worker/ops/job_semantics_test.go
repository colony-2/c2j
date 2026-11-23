package ops

import (
	"context"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Saved tasks can bypass the compiler's fresh input builder. Reject them before
// preparation or invocation even after a worker replacement.
func TestExactJobSavedTaskRejectsBeforePreparation(t *testing.T) {
	worker := &taskWorker{name: "recipe.await_result:wait_and_get", reg: ActivityRegistration{Metadata: coreops.OpMetadata{Type: "recipe.await_result"}}}
	for _, snapshot := range []ActivityInvocationRequest{
		{Input: map[string]any{"target": map[string]any{"job": "A"}}},
		{JobSemanticsVersion: 1, Input: map[string]any{"target": map[string]any{"job": "A"}}},
		{JobSemanticsVersion: 2, Input: map[string]any{"target": map[string]any{"job": "A"}, "wait": map[string]any{"on": "goal"}}},
		{JobSemanticsVersion: 3, Input: map[string]any{"target": map[string]any{"job": "A"}, "wait": map[string]any{"on": "job"}}},
	} {
		tc := wf.NewTaskContext(jobdb.JobKey{TenantId: "tenant", JobId: "job"}, 1, nil, nil, nil)
		active, starts, finishes := false, 0, 0
		local := task.WithActivityReporter(context.Background(), func(kind string) (func(string), func()) {
			require.Equal(t, worker.Name(), kind)
			require.False(t, active)
			active = true
			starts++
			return func(string) { t.Fatal("recipe wait must not report an extension") }, func() {
				require.True(t, active)
				active = false
				finishes++
			}
		})
		result, err := worker.RunTask(local, tc, jobdb.NewTaskDataOrPanic(snapshot))
		require.ErrorContains(t, err, "job_semantics_version")
		require.Nil(t, result)
		require.Equal(t, 1, starts)
		require.Equal(t, 1, finishes, "semantics rejection must finish the upstream health activity")
		require.False(t, active)
	}
}
