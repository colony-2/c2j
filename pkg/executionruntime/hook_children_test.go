package executionruntime

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestHookChildSubmissionReplacesForgedAuthority(t *testing.T) {
	wide := permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"stolen"}}, Jobs: permissions.JobPermissions{Submit: true}}
	start := workflowctl.StartJob{JobContext: contextual.JobContext{InheritedPermissions: &permissions.State{Version: 1, Grant: &wide}}}
	artifact := jobdb.NewArtifactFromBytes("recipe.yaml", []byte("id: child"))
	data, err := jobdb.NewTaskData(start, artifact)
	require.NoError(t, err)
	grant := permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"allowed"}}}
	job, err := constrainHookChild(jobdb.SubmitJob{JobType: "recipe", Data: data}, grant, json.RawMessage(`{"revision":"1"}`))
	require.NoError(t, err)
	raw, err := job.Data.GetData()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &start))
	state := start.JobContext.InheritedPermissions
	require.Equal(t, grant, *state.Limit)
	require.Error(t, state.CheckInput("stolen"))
	require.NoError(t, state.CheckInput("allowed"))
	require.JSONEq(t, `{"revision":"1"}`, string(state.ChildPolicy))
	arts, err := job.Data.GetArtifacts()
	require.NoError(t, err)
	require.Len(t, arts, 1)
	_, err = constrainHookChild(jobdb.SubmitJob{JobType: "other", Data: data}, grant, nil)
	require.Error(t, err)
}

type hookLeaseCapture struct {
	inner permissions.Issuer
	lease jobdb.ExecutionLease
}

func (i *hookLeaseCapture) IssueRestrictedLease(ctx context.Context, tc permissions.TaskDelegator, grant permissions.JobDB, deadline time.Time) (jobdb.ExecutionLease, error) {
	lease, err := i.inner.IssueRestrictedLease(ctx, tc, grant, deadline)
	i.lease = lease
	return lease, err
}
func TestHookRemoteTokenCannotBypassControllerOrReadConfiguration(t *testing.T) {
	ctx := context.Background()
	base := toy.New(toy.WithSecretProvider(&freshGitProvider{}))
	server := httptest.NewServer(remote.NewServer(base))
	defer server.Close()
	client, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	rt := New(client, execution.Allocation{})
	capture := &hookLeaseCapture{inner: rt.Issuer}
	rt.Issuer = capture
	task := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
		rt.mu.Lock()
		owner := rt.leases[tc.JobKey]
		rt.mu.Unlock()
		grant := permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"model"}}, Jobs: permissions.JobPermissions{Submit: true}}
		access, _, cancel, err := rt.restrictedAccess(local, tc, owner, grant, nil, permissions.State{Version: 1, ChildPolicy: json.RawMessage(`{"revision":"1"}`)})
		require.NoError(t, err)
		defer cancel()
		_, err = access.Resolve(local, []string{"model"})
		require.NoError(t, err)
		require.NotNil(t, capture.lease)
		_, err = capture.lease.GetConfiguration(local)
		require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
		child := jobdb.SubmitJob{TenantId: "test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(workflowctl.StartJob{RecipeName: "child"})}
		_, err = capture.lease.SubmitJob(local, jobdb.SubmitJobRequest{Job: child})
		require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
		_, err = access.Submit(local, child)
		require.NoError(t, err, "controller must still permit constrained submission")
		return in, nil
	})
	worker := wf.ExecutionWorker{JobType: "hook-delegation", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
		return c.DoTask(wf.TaskCall{Site: "probe", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return in, nil })
	}}
	h, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: worker.Name(), Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "test", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := run.Run(nil)
	require.NoError(t, err)
	require.Equal(t, wf.JobRunCompleted, out.Status, "%+v", out)
}
