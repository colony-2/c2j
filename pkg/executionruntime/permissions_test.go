package executionruntime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type delegationTask func(wf.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)

func (delegationTask) Name() string { return "delegate" }
func (f delegationTask) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return f(tc, in)
}

func TestRestrictedAccessUsesTaskDelegationAndOperationLimit(t *testing.T) {
	ctx := context.Background()
	base := toy.New(toy.WithSecretProvider(&freshGitProvider{}))
	server := httptest.NewServer(remote.NewServer(base))
	defer server.Close()
	client, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	rt := New(client, execution.Allocation{})
	issued := &recordingPermissionIssuer{inner: rt.Issuer}
	rt.Issuer = issued
	var saved wf.TaskContext
	var delegated jobdb.ExecutionLease
	var preparedAt time.Time
	task := delegationTask(func(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
		saved = tc
		rt.mu.Lock()
		owner := rt.leases[tc.JobKey]
		rt.mu.Unlock()
		grant := permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"model"}}, Jobs: permissions.JobPermissions{Submit: true}}
		access, _, cancel, err := rt.restrictedAccess(tc, owner, grant, nil)
		require.NoError(t, err)
		defer cancel()
		call, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		_, err = access.Resolve(call, []string{"model"})
		require.NoError(t, err)
		require.Len(t, issued.deadlines, 1)
		deadline := issued.deadlines[0]
		require.True(t, deadline.After(time.Now().Add(30*time.Second)), "RPC timeout must not become token lifetime")
		require.False(t, deadline.After(preparedAt.Add(time.Minute)), "preparation must consume the enclosing budget")
		_, err = access.Resolve(ctx, []string{"model"})
		require.NoError(t, err)
		require.Len(t, issued.deadlines, 1, "ordinary calls reuse the same child")
		_, err = access.Resolve(ctx, []string{"rw"})
		require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
		_, err = access.Submit(ctx, jobdb.SubmitJob{TenantId: "test", JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil)})
		require.NoError(t, err)

		limit := time.Now().UTC().Add(10 * time.Second)
		stopLimit := access.LimitDeadline(limit)
		defer stopLimit()
		_, err = access.Resolve(call, []string{"model"})
		require.NoError(t, err)
		require.Len(t, issued.deadlines, 2)
		require.Equal(t, limit, issued.deadlines[1], "explicit operation limit must narrow the cached child")
		stopLimit()
		_, err = access.Resolve(ctx, []string{"model"})
		require.ErrorIs(t, err, context.Canceled)
		require.Len(t, issued.deadlines, 2, "closed scope must not issue again")

		// A copied TaskContext retains its capability, and returned tokens can be
		// exported/imported without exposing the owner to the receiver.
		delegated, err = (permissions.JobDBIssuer{}).IssueRestrictedLease(ctx, saved, grant, time.Time{})
		require.NoError(t, err)
		capability, err := remote.ExportLease(delegated)
		require.NoError(t, err)
		imported, err := client.ImportLease(ctx, capability)
		require.NoError(t, err)
		_, err = imported.ResolveSecrets(ctx, []string{"model"})
		require.NoError(t, err)
		denied, err := imported.ResolveSecrets(ctx, []string{"rw"})
		require.NoError(t, err)
		require.Empty(t, denied.Secrets, "JobDB filters unauthorized secret names")
		return in, nil
	})
	worker := wf.ExecutionWorker{JobType: "delegation", Execute: func(c wf.ExecutionContext, in jobdb.JobData) (jobdb.JobData, error) {
		return c.DoScope("outer", wf.ScopeOptions{AttemptTimeout: time.Minute}, func(s wf.ExecutionContext) (jobdb.JobData, error) {
			timeout := jobdb.Duration(time.Hour)
			call := wf.TaskCall{Site: "delegate", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}
			call.Policy.InvocationTimeout = &timeout
			return s.DoTask(call, func(context.Context) (jobdb.TaskData, error) {
				time.Sleep(10 * time.Millisecond)
				preparedAt = time.Now()
				return in, nil
			})
		})
	}}
	handle, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "test", JobType: worker.Name(), Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "owner", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := run.Run(nil)
	require.NoError(t, err)
	require.Equal(t, wf.JobRunCompleted, out.Status, "%+v", out)
	_, err = saved.CreateDelegation(ctx, jobdb.CreateDelegationRequest{})
	require.Error(t, err, "closed task cannot create another child")
	_, err = delegated.ResolveSecrets(ctx, []string{"model"})
	require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
}

func TestRuntimeDelegationUnsupported(t *testing.T) {
	rt := New(toy.New(), execution.Allocation{})
	_, err := rt.CreateDelegation(context.Background(), nil, jobdb.CreateDelegationRequest{})
	require.ErrorIs(t, err, jobdb.ErrLeaseDelegationUnsupported)
}
