package task

import (
	"context"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"testing"
)

type activityMiddleware struct{ next wf.TaskWorker }

func (w activityMiddleware) Name() string { return w.next.Name() }
func (w activityMiddleware) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w activityMiddleware) RunTask(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return RunLocal(w.next, local, tc, in)
}

type activityWorker struct{}

func (activityWorker) Name() string { return "op:step" }
func (w activityWorker) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w activityWorker) RunTask(local context.Context, _ wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	update, finish := BeginActivity(local, w.Name())
	defer finish()
	update("./op")
	return nil, nil
}

func TestHealthActivityContextComposition(t *testing.T) {
	calls := 0
	ctx := WithActivityReporter(context.Background(), func(kind string) (func(string), func()) {
		require.Equal(t, "op:step", kind)
		calls++
		return func(selector string) { require.Equal(t, "./op", selector); calls++ }, func() { calls++ }
	})
	worker := activityMiddleware{activityMiddleware{activityWorker{}}}
	_, err := RunLocal(worker, ctx, wf.TaskContext{}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
	_, err = RunLocal(worker, context.Background(), wf.TaskContext{}, nil)
	require.NoError(t, err)
	require.Equal(t, 3, calls)
}
