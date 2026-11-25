package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/gitprofile"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func lateRealWorker(t *testing.T, rec recipe.Recipe, f *lateGitFixture) jobworkflow.ExecutionWorker {
	t.Helper()
	return lateHookFormatWorker(t, rec, f, false)
}

func lateHookFormatWorker(t *testing.T, rec recipe.Recipe, f *lateGitFixture, executionHookPolicy bool) jobworkflow.ExecutionWorker {
	t.Helper()
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "test"}, GitBase: contextual.GitBaseContext{BaseRepo: f.dir, BaseRef: f.initial, ResolvedBaseHash: f.initial}}
	return jobworkflow.ExecutionWorker{JobType: "late-include-test", Execute: func(exec jobworkflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
		out, arts, err := ExecuteRecipe(workflow.Context{JobContext: exec, ExecutionHookPolicy: executionHookPolicy, ServiceDependencies2: catchWorkflowContext(exec).ServiceDependencies2}, rec, map[string]any{}, job, contextual.GitCommitContext{ParentRef: f.initial, ParentHash: f.initial, PersistHash: f.initial})
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(out, arts...)
	}}
}
func lateRealTasks(t *testing.T, guard *executionruntime.Runtime, ws *jobworkflow.WorkSet) []jobworkflow.TaskWorker {
	var extra []jobworkflow.TaskWorker
	for name, w := range ws.TaskWorkers {
		if name != lateSelectTaskType && name != lateCaptureTaskType && name != CellContextTaskType {
			extra = append(extra, w)
		}
	}
	return lateTasks(t, guard, extra...)
}
func TestLateIncludeAuthorityAndRootTrust(t *testing.T) {
	type secretInput struct {
		Token  string `json:"token" secret:"true"`
		Direct string `json:"direct"`
	}
	for _, mode := range []string{"same-repo-inheritance", "same-repo-limit", "same-repo-authored-grant", "foreign-delegated", "foreign-no-delegation", "foreign-unauthorized-input", "foreign-value-not-name", "foreign-commit-allowance", "foreign-commit-no-root-trust"} {
		t.Run(mode, func(t *testing.T) {
			var effects atomic.Int32
			op := coreops.NewActivityMappedOpV2[secretInput, map[string]any](coreops.OpMetadata{Type: "late_secret_probe"}, func(_ coreops.OpDependencies, ctx context.Context, in secretInput) (map[string]any, error) {
				if in.Direct != "" {
					if _, err := permissions.FromContext(ctx).Resolve(ctx, []string{in.Direct}); err != nil {
						return nil, err
					}
				}
				if in.Token != "" && !strings.HasSuffix(in.Token, "-private-value\n") {
					return nil, fmt.Errorf("secret not materialized")
				}
				effects.Add(1)
				return map[string]any{"ok": true}, nil
			})
			ws := workspaceTestWorker(t, op)
			root := newLateGitFixture(t)
			target := root
			foreign := strings.HasPrefix(mode, "foreign")
			if foreign {
				target = newLateGitFixture(t)
			}
			input := "token: '${{ context.secrets.token }}'"
			if mode == "same-repo-inheritance" {
				input += ", direct: token"
			}
			if mode == "foreign-unauthorized-input" || mode == "foreign-commit-no-root-trust" {
				input = "token: '${{ context.secrets.stolen }}'"
			}
			leaf := commitHookFile(t, target.dir, "flows/leaf.yaml", "id: secret-leaf\nsequence:\n- id: effect\n  op: late_secret_probe\n  inputs: {"+input+"}\noutputs: {ok: '{{ sequence.effect.outputs.ok }}'}\n")
			selected := leaf
			path := "flows/leaf.yaml"
			if mode == "foreign-commit-allowance" || mode == "foreign-commit-no-root-trust" {
				selected = commitHookFile(t, target.dir, "flows/switch.yaml", fmt.Sprintf("id: foreign-parent\nsequence:\n- id: newer\n  include: {bind: late, recipe: %q}\noutputs: {ok: '{{ sequence.newer.outputs.ok }}'}\n", target.selector("flows/leaf.yaml", leaf)))
				path = "flows/switch.yaml"
			}
			callPerms := ""
			rootPerms := "perms: {jobdb: {secrets: {resolve: [token]}}}\n"
			if foreign && mode != "foreign-no-delegation" {
				callPerms = "  perms: {jobdb: {secrets: {resolve: [token]}}}\n"
			}
			if mode == "same-repo-limit" {
				callPerms = "  perms: {limit: {jobdb: {}}}\n"
			}
			if mode == "same-repo-authored-grant" || mode == "foreign-value-not-name" {
				callPerms = "  perms: {jobdb: {}}\n"
			}
			if mode == "foreign-value-not-name" {
				selected = commitHookFile(t, target.dir, "flows/leaf.yaml", "id: leaf\ninput_schema:\n bound: {type: string, required: true}\ninputs: {bound: '{{ inputs.bound }}'}\nsequence:\n- id: effect\n  op: late_secret_probe\n  inputs: {token: '${{ context.secrets.token }}'}\n")
				callPerms += "  inputs: {bound: supplied-secret-value}\n"
			}
			rec := root.expand(t, fmt.Sprintf("id: root\n%ssequence:\n- id: call\n  include: {bind: late, recipe: %q}\n%s", rootPerms, target.selector(path, selected), callPerms))
			provider := &testSecretProvider{}
			rt, err := sqliteruntime.NewFromConfig(lateTestContext(), sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db"), SecretProvider: provider, Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, err)
			defer rt.Close(testBackground())
			guard := lateGuard(rt)
			guard.Issuer = nil // SQLite uses its normal in-process per-call guard.
			w := lateRealWorker(t, rec, root)
			key := probeSubmit(t, rt, w)
			out := lateRun(t, guard, key, w, lateRealTasks(t, guard, ws)...)
			allowed := mode == "same-repo-inheritance" || mode == "foreign-delegated" || mode == "foreign-commit-allowance"
			if allowed {
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, int32(1), effects.Load())
				require.NotEmpty(t, provider.Calls())
			} else {
				require.Equal(t, jobworkflow.JobRunFailed, out.Status, "unexpected permission success")
				require.Zero(t, effects.Load())
				require.Empty(t, provider.Calls())
			}
			s, c, _ := lateRecordOutputs(t, rt, key)
			if mode == "foreign-no-delegation" {
				require.Empty(t, s)
				require.Empty(t, c)
				require.Zero(t, target.reads.Load())
			}
			if len(s) > 0 {
				require.Equal(t, foreign, s[0].PermissionBoundary)
				require.Equal(t, !foreign, s[0].TrustedSource)
			}
			if len(s) == 2 {
				require.False(t, s[1].PermissionBoundary)
				require.False(t, s[1].TrustedSource)
				require.Equal(t, s[0].Declaration.RootRepository, s[1].Declaration.RootRepository)
			}
		})
	}
}
func TestLateIncludeGitAuthorityBeforeFetch(t *testing.T) {
	for _, mode := range []string{"unavailable-secret", "caller-limit", "unknown-profile", "nested-ceiling"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nsequence: []\n")
			call := "  git_profile: private\n"
			rootPerms := ""
			if mode == "caller-limit" {
				rootPerms = "perms: {limit: {jobdb: {}}}\n"
			}
			if mode == "unknown-profile" {
				call = "  git_profile: absent\n"
			}
			profile, _ := json.Marshal(gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: f.url, Username: "bot", SecretRef: "missing"}}})
			rt := newTimeoutReproRuntime(t, "sqlite")
			_, err := rt.ConsumerRuntime.(interface {
				SetTenantConfigurationValue(context.Context, jobdb.SetTenantConfigurationValueRequest) (string, error)
			}).SetTenantConfigurationValue(testBackground(), jobdb.SetTenantConfigurationValueRequest{TenantID: "scope-tests", Name: gitprofile.ProfilePrefix + "private", Value: profile})
			require.NoError(t, err)
			if mode == "nested-ceiling" {
				// Bootstrap uses the caller's profile; the callee's ceiling must block a
				// nested include selecting a credential-bearing profile before its read.
				child := commitHookFile(t, f.dir, "grand.yaml", "id: grand\nsequence: []\n")
				commit = commitHookFile(t, f.dir, "child.yaml", fmt.Sprintf("id: child\nsequence:\n- id: grand\n  include: %q\n  git_profile: private\n", f.selector("grand.yaml", child)))
				call = "  perms: {limit: {jobdb: {}}}\n"
			}
			rec := f.expand(t, fmt.Sprintf("id: root\n%ssequence:\n- id: call\n  include: {bind: late, recipe: %q}\n%s", rootPerms, f.selector("child.yaml", commit), call))
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			tasks := lateTasks(t, guard)
			resolver := &lateCredentialResolver{RecipeSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{SelectorCache: &selectorcache.Cache{Root: t.TempDir()}}), repository: f.url}
			if mode == "nested-ceiling" {
				resolver.path = "grand.yaml"
			}
			for i, task := range tasks {
				if task.Name() == lateCaptureTaskType {
					tasks[i] = guard.WrapTaskWorker(lateCaptureWorker{resolver: resolver})
				}
			}
			out := lateRun(t, guard, key, w, tasks...)
			require.Equal(t, jobworkflow.JobRunFailed, out.Status)
			if mode != "nested-ceiling" {
				require.Zero(t, f.reads.Load())
			}
		})
	}
}

// Require the actual Git profile credential RPC, then use the real Git loader.
// The credential response and source/capture outcomes are never fabricated.
type lateCredentialResolver struct {
	RecipeSourceResolver
	repository, path string
}

func (r *lateCredentialResolver) LoadYAML(ctx context.Context, project string, resolution RecipeSourceResolution) ([]byte, error) {
	if r.path == "" || strings.Contains(resolution.EffectiveSelector(), r.path) {
		var socket string
		for _, entry := range gitprofile.Env(ctx, os.Environ()) {
			if v, ok := strings.CutPrefix(entry, gitprofile.SocketEnv+"="); ok {
				socket = v
			}
		}
		repo, err := url.Parse(r.repository)
		if err != nil {
			return nil, err
		}
		if _, err := gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "credential", Protocol: repo.Scheme, Host: repo.Host, Path: strings.TrimPrefix(repo.Path, "/")}); err != nil {
			return nil, err
		}
	}
	return r.RecipeSourceResolver.(RecipeSourceYAMLLoader).LoadYAML(ctx, project, resolution)
}
