package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type inheritanceSecrets struct{ name string }

func (s inheritanceSecrets) ResolveSecrets(_ context.Context, req jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	out := jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{}}
	for _, name := range req.Names {
		if name == s.name {
			out.Secrets[name] = jobdb.SecretValue{Value: "synthetic-git-password"}
		}
	}
	return out, nil
}

type inheritanceIssuer struct {
	mu   sync.Mutex
	last jobdb.ExecutionLease
}

func (i *inheritanceIssuer) IssueRestrictedLease(ctx context.Context, tc permissions.TaskDelegator, grant permissions.JobDB, deadline time.Time) (jobdb.ExecutionLease, error) {
	l, err := (permissions.JobDBIssuer{}).IssueRestrictedLease(ctx, tc, grant, deadline)
	if err == nil {
		i.mu.Lock()
		i.last = l
		i.mu.Unlock()
	}
	return l, err
}

// Exercise recipe compilation, durable handoff, the runtime guard, actual Git
// smart HTTP, the CLI credential helper, and JobDB delegation without a model.
func TestPermissionInheritanceAuthenticatedGitAndResume(t *testing.T) {
	t.Setenv("C2J_GIT_HELPER_TEST", "1")
	previous := coreops.List()
	t.Cleanup(func() { coreops.Replace(previous...) })
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "c2j helper")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(os.Args[0], "'", "'\\''") + "' -test.run=TestGitCredentialHelperProcess -- \"$@\"\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
	runGit := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", args...).CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	repo := filepath.Join(dir, "source")
	runGit("init", "-b", "main", repo)
	runGit("-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial")
	runGit("-C", repo, "config", "user.name", "Test")
	runGit("-C", repo, "config", "user.email", "test@example.com")
	runGit("clone", "--bare", repo, filepath.Join(dir, "private.git"))
	head := runGit("-C", repo, "rev-parse", "HEAD")
	backendPath := filepath.Join(runGit("--exec-path"), "git-http-backend")
	if _, err := os.Stat(backendPath); os.IsNotExist(err) {
		t.Skip("Git smart HTTP backend not installed")
	}
	backend := &cgi.Handler{Path: backendPath, Root: "/", Env: []string{"GIT_PROJECT_ROOT=" + dir, "GIT_HTTP_EXPORT_ALL=1"}}
	var authenticated atomic.Int32
	gitServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "bot" || password != "synthetic-git-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("service") == "git-receive-pack" || strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			http.Error(w, "read only", http.StatusForbidden)
			return
		}
		authenticated.Add(1)
		backend.ServeHTTP(w, r)
	}))
	defer gitServer.Close()
	for _, backendName := range []string{"toy", "remote"} {
		for _, secretName := range []string{"deployment_a_git", "deployment_b_git"} {
			t.Run(backendName+"/"+secretName, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), time.Minute)
				defer cancel()
				base := toy.New(toy.WithSecretProvider(inheritanceSecrets{name: secretName}))
				var runtime jobdb.WorkflowRuntime = base
				if backendName == "remote" {
					server := httptest.NewServer(celltest.Handler(remote.NewServer(base)))
					defer server.Close()
					var err error
					runtime, err = remote.New(server.URL, server.Client())
					require.NoError(t, err)
				}
				profile, err := json.Marshal(gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: gitServer.URL + "/", Username: "bot", SecretRef: secretName}}})
				require.NoError(t, err)
				for name, value := range map[string]json.RawMessage{gitprofile.ReadOnlyKey: json.RawMessage(`"read"`), gitprofile.ProfilePrefix + "read": profile} {
					_, err := base.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "test", Name: name, Value: value})
					require.NoError(t, err)
				}
				issuer := &inheritanceIssuer{}
				var observed []string
				type probeInput struct {
					ID    string `json:"id"`
					Allow bool   `json:"allow"`
				}
				probe := func(deps coreops.OpDependencies, ctx context.Context, in probeInput) (map[string]any, error) {
					access := permissions.FromContext(ctx)
					if access == nil || access.Grant().Jobs.Submit != in.Allow {
						return nil, fmt.Errorf("unexpected submit grant for %s", in.ID)
					}
					cloneDir := filepath.Join(t.TempDir(), "clone")
					// The wrapper invokes the actual CLI helper from this test binary.
					// The runtime-created socket, profile, and resolver are unchanged.
					helper := "!'" + strings.ReplaceAll(wrapper, "'", "'\\''") + "' secret git-credential"
					clone := gitprofile.Command(ctx, "-c", "credential.helper=", "-c", "credential.helper="+helper, "clone", gitServer.URL+"/private.git", cloneDir)
					if out, err := clone.CombinedOutput(); err != nil {
						return nil, fmt.Errorf("authenticated clone: %w: %s", err, out)
					}
					out, err := exec.CommandContext(ctx, "git", "-C", cloneDir, "rev-parse", "HEAD").Output()
					if err != nil || strings.TrimSpace(string(out)) != head {
						return nil, fmt.Errorf("clone did not check out the expected commit: %v", err)
					}
					job := jobdb.SubmitJob{TenantId: "test", JobType: "restriction-child", Cell: "test", CellRepo: celltest.Repo(repo), Data: jobdb.NewTaskDataOrPanic(nil)}
					if !in.Allow && backendName == "remote" {
						issuer.mu.Lock()
						delegated := issuer.last
						issuer.mu.Unlock()
						if delegated == nil {
							return nil, fmt.Errorf("clone did not issue a delegated credential")
						}
						if _, err := delegated.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job}); !errors.Is(err, jobdb.ErrLeasePermissionDenied) {
							return nil, fmt.Errorf("delegated submission was not denied: %v", err)
						}
					}
					_, err = deps.JobTool().(*coreops.TaskBasedJobTool).SubmitJobRef(ctx, job)
					if (in.Allow && err != nil) || (!in.Allow && !errors.Is(err, jobdb.ErrLeasePermissionDenied)) {
						return nil, fmt.Errorf("unexpected guarded submission result: %v", err)
					}
					info := selfinfo.FromContext(ctx)()
					info.SetCapabilities(true, true, true)
					if in.Allow != (len(info.Capabilities["jobs.submit"].Restrictions) == 0) {
						return nil, fmt.Errorf("stale or missing restriction diagnostic")
					}
					observed = append(observed, in.ID)
					return map[string]any{"ok": true}, nil
				}
				coreops.Replace(coreops.NewActivityMappedOpV2[probeInput, map[string]any](coreops.OpMetadata{Type: "restriction_probe"}, probe))
				source := fmt.Sprintf(`id: restrictions
version: "1"
sequence:
  - id: restricted
    perms: {inherit: true, jobdb: {jobs: {submit: false}}}
    sequence:
      - id: fresh
        op: restriction_probe
        inputs: {id: fresh}
      - id: resumed
        execution_needs: {capabilities: [resume]}
        op: restriction_probe
        inputs: {id: resumed}
      - id: reenabled
        perms: {inherit: true, jobdb: {jobs: {submit: true}}}
        op: restriction_probe
        inputs: {id: reenabled, allow: true}
      - id: replacement
        perms: {jobdb: {jobs: {submit: true}, secrets: {resolve: [%s]}}}
        op: restriction_probe
        inputs: {id: replacement, allow: true}
      - id: inherited
        op: restriction_probe
        inputs: {id: inherited}
  - id: sibling
    op: restriction_probe
    inputs: {id: sibling, allow: true}
`, secretName)
				rec, err := recipe.LoadRecipeFromString([]byte(source))
				require.NoError(t, err)
				celltest.Create(t, runtime.(jobdb.JobSchemaRegistry), "test", "test", celltest.Repo(repo))
				engine, err := wf.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
				require.NoError(t, err)
				ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "test", RecipeName: "restrictions", JobContext: contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}, Cell: "test", CellRepo: celltest.Repo(repo)}, jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime.(jobdb.JobSchemaRegistry)}, *rec)
				require.NoError(t, err)
				run := func(capabilities ...string) wf.JobRunOutcome {
					allocation := execution.Allocation{SchemaVersion: 1, Capabilities: capabilities}
					guard := executionruntime.New(runtime, allocation)
					if backendName == "remote" {
						guard.Issuer = issuer
					}
					registry, err := workerops.NewActivityRegistry()
					require.NoError(t, err)
					ws, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{Allocation: allocation, WrapTaskWorker: guard.WrapTaskWorker, StageExecution: guard.Stage})
					require.NoError(t, err)
					var tasks []wf.TaskWorker
					for _, task := range ws.TaskWorkers {
						tasks = append(tasks, task)
					}
					runnable, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "restriction-test", LeaseDuration: time.Minute})
					require.NoError(t, err)
					out, err := runnable.Run(nil)
					require.NoError(t, err)
					return out
				}
				first := run()
				require.Equal(t, wf.JobRunSuspended, first.Status, "%+v", first)
				require.Equal(t, []string{"fresh"}, observed)
				jobs, err := runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"test"}})
				require.NoError(t, err)
				require.Len(t, jobs.Jobs, 1, "denied submissions must not create jobs")
				second := run("resume")
				require.Equal(t, wf.JobRunCompleted, second.Status, "%+v", second)
				require.Equal(t, []string{"fresh", "resumed", "reenabled", "replacement", "inherited", "sibling"}, observed, "completed work must replay without executing again")
				jobs, err = runtime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"test"}})
				require.NoError(t, err)
				require.Len(t, jobs.Jobs, 4, "only the enabled scopes and sibling may create children")
			})
		}
	}
	require.Positive(t, authenticated.Load())
}
