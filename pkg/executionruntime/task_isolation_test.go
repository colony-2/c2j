package executionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type connectionSecretProvider int

func (p connectionSecretProvider) ResolveSecrets(_ context.Context, req jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	values := map[string]jobdb.SecretValue{}
	for _, name := range req.Names {
		values[name] = jobdb.SecretValue{Value: fmt.Sprintf("private-%d-%s", p, name)}
	}
	return jobdb.SecretResolveResult{Secrets: values}, nil
}

func TestTaskStateIsolatedAcrossConnectionsAndSuccessiveTasks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type taskState struct {
		slot     int
		scope    *gitprofile.Scope
		redactor *secrets.Redactor
		socket   string
	}
	ready := make(chan taskState, 2)
	release := []chan struct{}{make(chan struct{}), make(chan struct{})}
	const markers = "private-0-model private-1-model private-0-git private-1-git"
	credential := gitprofile.Request{Operation: "credential", Protocol: "https", Host: "git.example", Path: "repo.git"}
	// Both connections use this same worker, tenant/job IDs, task sites, and
	// attempt numbers. Only explicitly supplied invocation state may differ.
	worker := delegationTask(func(local context.Context, tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
		var request struct {
			Phase string `json:"phase"`
		}
		raw, err := in.GetData()
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		redactor, scope := secrets.RedactorFromContext(local), gitprofile.FromContext(local)
		if redactor == nil || scope == nil || redactor.Redact(markers) != markers {
			return nil, fmt.Errorf("task inherited secret state or is missing local resources")
		}
		values, err := permissions.FromContext(local).Resolve(local, []string{"model"})
		if err != nil {
			return nil, err
		}
		model := values.Secrets["model"].Value
		slot := 0
		if model == "private-1-model" {
			slot = 1
		} else if model != "private-0-model" {
			return nil, fmt.Errorf("wrong connection's secret")
		}
		var socket string
		for _, env := range gitprofile.Env(local, nil) {
			if value, ok := strings.CutPrefix(env, gitprofile.SocketEnv+"="); ok {
				socket = value
			}
		}
		git, err := gitprofile.Call(local, socket, credential)
		if err != nil {
			return nil, err
		}
		if git.Secret == nil || git.Secret.Value != fmt.Sprintf("private-%d-git", slot) {
			return nil, fmt.Errorf("credential came from the wrong connection")
		}
		if redactor.Redact(model+" "+git.Secret.Value) != "[REDACTED] [REDACTED]" || scope.Redact(model) != "[REDACTED]" {
			return nil, fmt.Errorf("ordinary and Git secrets did not share the task redactor")
		}
		tc.Logger.Info(markers)
		if request.Phase == "first" {
			ready <- taskState{slot, scope, redactor, socket}
			select {
			case <-release[slot]:
			case <-local.Done():
				return nil, local.Err()
			}
		}
		return jobdb.NewTaskDataOrPanic(true), nil
	})
	state := permissions.State{Version: permissions.Version, Grant: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"model", "git"}}}}
	orchestrator := wf.ExecutionWorker{JobType: "isolation", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		var out jobdb.TaskData
		for _, phase := range []string{"first", "second"} {
			command, _ := json.Marshal(phase)
			var err error
			out, err = c.DoTask(wf.TaskCall{Site: phase, TaskType: worker.Name(), ContractVersion: 1, Command: command,
				Policy: jobdb.RunPolicy{TotalTimeout: jobdb.AsDuration(time.Minute)}}, func(context.Context) (jobdb.TaskData, error) {
				return jobdb.NewTaskDataOrPanic(map[string]any{"phase": phase, "permissions": state}), nil
			})
			if err != nil {
				return nil, err
			}
		}
		return out, nil
	}}
	var logs [2]bytes.Buffer
	type runResult struct {
		out wf.JobRunOutcome
		err error
	}
	results := []chan runResult{make(chan runResult, 1), make(chan runResult, 1)}
	for slot := range 2 {
		base := toy.New(toy.WithSecretProvider(connectionSecretProvider(slot)))
		profile, err := json.Marshal(gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: "https://git.example/", Username: "bot", SecretRef: "git"}}})
		require.NoError(t, err)
		for name, value := range map[string]json.RawMessage{gitprofile.ReadOnlyKey: json.RawMessage(`"work"`), gitprofile.ProfilePrefix + "work": profile} {
			_, err = base.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "same", Name: name, Value: value})
			require.NoError(t, err)
		}
		server := httptest.NewServer(remote.NewServer(base))
		t.Cleanup(server.Close)
		client, err := remote.New(server.URL, server.Client())
		require.NoError(t, err)
		runtime := New(client, execution.Allocation{})
		job, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "same", JobID: "same", JobType: orchestrator.Name(), Data: jobdb.NewTaskDataOrPanic(nil)}})
		require.NoError(t, err)
		run, err := wf.GetJobForRun(ctx, runtime, wf.GetJobForRunRequest{JobKey: job.JobKey, JobWorker: orchestrator,
			TaskWorkers: []wf.TaskWorker{runtime.WrapTaskWorker(worker)}, WorkerID: "worker", LeaseDuration: time.Minute,
			Logger: slog.New(slog.NewTextHandler(&logs[slot], nil))})
		require.NoError(t, err)
		go func() { out, err := run.Run(nil); results[slot] <- runResult{out, err} }()
	}
	var states [2]taskState
	for range 2 {
		select {
		case state := <-ready:
			states[state.slot] = state
		case result := <-results[0]:
			t.Fatalf("first connection exited before barrier: %+v, %v", result.out, result.err)
		case result := <-results[1]:
			t.Fatalf("second connection exited before barrier: %+v, %v", result.out, result.err)
		case <-ctx.Done():
			t.Fatal("tasks did not reach barrier")
		}
	}
	require.NotSame(t, states[0].scope, states[1].scope)
	require.NotSame(t, states[0].redactor, states[1].redactor)
	require.NotEqual(t, states[0].socket, states[1].socket)
	_, err := gitprofile.Call(ctx, states[0].socket, gitprofile.Request{Operation: "clear"})
	require.NoError(t, err)
	_, err = gitprofile.Call(ctx, states[0].socket, credential)
	require.Error(t, err)
	_, err = gitprofile.Call(ctx, states[1].socket, credential)
	require.NoError(t, err, "clearing one task must not affect another connection")
	for slot := range 2 {
		close(release[slot])
		result := <-results[slot]
		require.NoError(t, result.err)
		require.Equal(t, wf.JobRunCompleted, result.out.Status, "%v", result.out.JobError)
		require.NotContains(t, logs[slot].String(), fmt.Sprintf("private-%d-", slot))
		require.Contains(t, logs[slot].String(), fmt.Sprintf("private-%d-model", 1-slot), "unrelated tasks must not supply log masks")
		_, err = os.Stat(states[slot].socket)
		require.True(t, os.IsNotExist(err), "task socket must be removed")
		require.ErrorIs(t, states[slot].redactor.Register("late"), jobdb.ErrExecutionLeaseLost)
	}
}
