package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type freshGitProvider struct{ calls atomic.Int32 }

func (p *freshGitProvider) ResolveSecrets(ctx context.Context, req jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	n := p.calls.Add(1)
	values := map[string]jobdb.SecretValue{}
	for _, name := range req.Names {
		values[name] = jobdb.SecretValue{Value: fmt.Sprintf("credential-%s-%d", name, n)}
	}
	return jobdb.SecretResolveResult{Secrets: values}, nil
}
func TestRecipeGitProfilesAcrossRenewalAndScopeChanges(t *testing.T) {
	previous := coreops.List()
	t.Cleanup(func() { coreops.Replace(previous...) })
	type input struct {
		Want  string `json:"want"`
		Wait  bool   `json:"wait,omitempty"`
		Model bool   `json:"model,omitempty"`
		Deny  bool   `json:"deny,omitempty"`
	}
	var observed []string
	probe := func(deps coreops.OpDependencies, ctx context.Context, in input) (map[string]any, error) {
		var socket string
		for _, entry := range gitprofile.Env(ctx, os.Environ()) {
			if v, ok := strings.CutPrefix(entry, gitprofile.SocketEnv+"="); ok {
				socket = v
			}
		}
		if socket == "" {
			return nil, fmt.Errorf("missing execution socket")
		}
		request := gitprofile.Request{Operation: "credential", Protocol: "https", Host: "git.example", Path: "repo.git"}
		a, err := gitprofile.Call(ctx, socket, request)
		if in.Want == "none" {
			if err == nil {
				return nil, fmt.Errorf("cleared scope supplied credentials")
			}
		} else {
			if err != nil {
				return nil, err
			}
			if !strings.Contains(a.Secret.Value, "credential-"+in.Want+"-") {
				return nil, fmt.Errorf("wrong profile selected")
			}
			if in.Wait {
				select {
				case <-time.After(250 * time.Millisecond):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			b, err := gitprofile.Call(ctx, socket, request)
			if err != nil {
				return nil, err
			}
			if a.Secret.Value == b.Secret.Value {
				return nil, fmt.Errorf("credential was cached")
			}
		}
		if in.Want != "rw" {
			if _, err := gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "secret", Name: "rw"}); err == nil {
				return nil, fmt.Errorf("RO scope obtained RW secret")
			}
		}
		if access := permissions.FromContext(ctx); access == nil {
			return nil, fmt.Errorf("missing permission guard")
		} else {
			if _, err := gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "config", Name: gitprofile.ReadWriteKey}); err != nil {
				return nil, fmt.Errorf("config read denied in restricted scope: %w", err)
			}
			_, err := gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "secret", Name: "model"})
			if in.Model != (err == nil) {
				return nil, fmt.Errorf("unexpected model secret access: %v", err)
			}
			if !in.Deny {
				_, err = deps.JobTool().(*coreops.TaskBasedJobTool).SubmitJobRef(ctx, jobdb.SubmitJob{TenantId: "test", JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil)})
				if err != nil {
					return nil, fmt.Errorf("default submission denied: %w", err)
				}
			}
			if in.Deny {
				_, err = deps.JobTool().(*coreops.TaskBasedJobTool).SubmitJobRef(ctx, jobdb.SubmitJob{TenantId: "test", JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil)})
				if !errors.Is(err, jobdb.ErrLeasePermissionDenied) {
					return nil, fmt.Errorf("submission was not denied: %v", err)
				}
			}
		}
		observed = append(observed, in.Want)
		return map[string]any{"ok": true}, nil
	}
	coreops.Replace(coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "git_profile_probe"}, probe), coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "merge"}, probe), coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "squashrebasemerge"}, probe))
	for _, backend := range []string{"toy", "remote", "toy-permissions", "remote-permissions"} {
		t.Run(backend, func(t *testing.T) {
			observed = nil
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			provider := &freshGitProvider{}
			base := toy.New(toy.WithSecretProvider(provider))
			var runtime jobdb.WorkflowRuntime = base
			if strings.HasPrefix(backend, "remote") {
				server := httptest.NewServer(remote.NewServer(base))
				defer server.Close()
				remoteRT, err := remote.New(server.URL, server.Client())
				require.NoError(t, err)
				runtime = remoteRT
			}
			for _, name := range []string{"ro", "rw"} {
				p := gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: "https://git.example/", Username: "bot", SecretRef: name}}}
				raw, _ := json.Marshal(p)
				_, err := base.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "test", Name: gitprofile.ProfilePrefix + name, Value: raw})
				require.NoError(t, err)
			}
			for key, name := range map[string]string{gitprofile.ReadOnlyKey: "ro", gitprofile.ReadWriteKey: "rw"} {
				raw, _ := json.Marshal(name)
				_, err := base.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "test", Name: key, Value: raw})
				require.NoError(t, err)
			}
			repo := t.TempDir()
			for _, args := range [][]string{{"init", "-b", "main", repo}, {"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial"}} {
				out, err := exec.Command("git", args...).CombinedOutput()
				require.NoError(t, err, string(out))
			}
			// Identity is local so no developer Git configuration is needed.
			require.NoError(t, exec.Command("git", "-C", repo, "config", "user.name", "Test").Run())
			require.NoError(t, exec.Command("git", "-C", repo, "config", "user.email", "test@example.com").Run())
			text := `id: profiles
sequence:
  - id: read
    op: git_profile_probe
    inputs: {want: ro, wait: true}
  - id: publish
    op: merge
    inputs: {want: rw}
  - id: after
    op: git_profile_probe
    inputs: {want: ro}
  - id: isolated
    git_profile: null
    sequence:
      - id: denied
        op: merge
        inputs: {want: none}
  - id: explicit
    git_profile: ro
    op: squashrebasemerge
    inputs: {want: ro}
  - id: restored
    op: git_profile_probe
    inputs: {want: ro}
`
			if strings.HasSuffix(backend, "permissions") {
				text = strings.Replace(text, "id: profiles\n", "id: profiles\nperms: {}\ntimeout: 10s\n", 1)
				text = strings.Replace(text, "  - id: read\n", "  - id: read\n    timeout: 1m\n", 1)
				text += `  - id: model
    op: git_profile_probe
    perms: {jobdb: {secrets: {resolve: [model, ro]}}}
    inputs: {want: ro, model: true, deny: true}
  - id: deny
    op: git_profile_probe
    perms: {jobdb: {}}
    inputs: {want: none, deny: true}
  - id: sibling
    op: git_profile_probe
    inputs: {want: ro}
`
			}
			rec, err := recipe.LoadRecipeFromString([]byte(text))
			require.NoError(t, err)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
			require.NoError(t, err)
			keyRef, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "test", RecipeName: "profiles", JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "repo"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}, jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime.(jobdb.JobSchemaRegistry)}, *rec)
			key := keyRef.Key()
			require.NoError(t, err)
			rt := New(runtime, execution.Allocation{SchemaVersion: 1})
			var issued *recordingPermissionIssuer
			if strings.HasPrefix(backend, "remote") {
				issued = &recordingPermissionIssuer{inner: rt.Issuer}
				rt.Issuer = issued
			}
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			deps := coreops.NewServiceDepsBuilder().Build()
			ws, err := compiler.NewRecipeWorkerWithOptions(deps, registry, compiler.RecipeJobWorkerOptions{Allocation: execution.Allocation{SchemaVersion: 1}, WrapTaskWorker: rt.WrapTaskWorker, StageExecution: rt.Stage})
			require.NoError(t, err)
			var taskWorkers []jobworkflow.TaskWorker
			for _, worker := range ws.TaskWorkers {
				taskWorkers = append(taskWorkers, worker)
			}
			runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: taskWorkers, WorkerID: "profiles", LeaseDuration: 150 * time.Millisecond})
			require.NoError(t, err)
			out, err := runnable.Run(nil)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, fmt.Sprintf("%+v", out))
			if strings.HasSuffix(backend, "permissions") {
				require.Equal(t, []string{"ro", "rw", "ro", "none", "ro", "ro", "ro", "none", "ro"}, observed)
				require.EqualValues(t, 15, provider.calls.Load())
				if issued != nil {
					issued.mu.Lock()
					defer issued.mu.Unlock()
					require.NotEmpty(t, issued.deadlines)
					for _, deadline := range issued.deadlines {
						require.Equal(t, issued.deadlines[0], deadline, "preparation and sibling tasks must not restart the enclosing deadline")
						require.WithinDuration(t, time.Now(), deadline, 10*time.Second, "enclosing timeout must beat the op's one-minute timeout")
					}
				}
			} else {
				require.Equal(t, []string{"ro", "rw", "ro", "none", "ro", "ro"}, observed)
				require.EqualValues(t, 10, provider.calls.Load())
				if issued != nil {
					issued.mu.Lock()
					defer issued.mu.Unlock()
					require.NotEmpty(t, issued.deadlines, "recipes without perms must issue restricted credentials")
					for _, deadline := range issued.deadlines {
						require.True(t, deadline.After(time.Now()))
						require.WithinDuration(t, time.Now(), deadline, time.Hour)
					}
				}
			}
		})
	}
}

// Capture only policy bounds, never credential material.
type recordingPermissionIssuer struct {
	inner     permissions.Issuer
	mu        sync.Mutex
	deadlines []time.Time
}

func (i *recordingPermissionIssuer) IssueRestrictedLease(ctx context.Context, task permissions.TaskDelegator, grant permissions.JobDB, deadline time.Time) (jobdb.ExecutionLease, error) {
	child, err := i.inner.IssueRestrictedLease(ctx, task, grant, deadline)
	if err == nil {
		info := child.(interface{ Delegation() *jobdb.DelegationInfo }).Delegation()
		i.mu.Lock()
		i.deadlines = append(i.deadlines, *info.NotAfter)
		i.mu.Unlock()
	}
	return child, err
}
