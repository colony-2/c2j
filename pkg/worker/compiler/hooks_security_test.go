package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func hookTestEngine(t *testing.T, ws *jobworkflow.WorkSet) (context.Context, jobworkflow.Engine, *toyruntime.Runtime, *testSecretProvider) {
	t.Helper()
	provider := &testSecretProvider{}
	rt := toyruntime.New(toyruntime.WithSecretProvider(provider))
	guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
	var workers []jobworkflow.TaskWorker
	for _, w := range ws.TaskWorkers {
		workers = append(workers, guard.WrapTaskWorker(w))
	}
	eng, err := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("hooks").PlusWorkers(ws.JobWorker, workers...).BuildEngine()
	require.NoError(t, err)
	eng = jobdbschema.WorkflowEngine{Engine: eng, Registry: rt}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go eng.Run(ctx)
	return ctx, eng, rt, provider
}
func startHookTest(t *testing.T, ctx context.Context, eng jobworkflow.Engine, repo, body string) jobdb.JobKey {
	t.Helper()
	rec, err := recipe.LoadRecipeFromString([]byte(body))
	require.NoError(t, err)
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "hooks", RecipeName: rec.GetMetadata().ID, JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}, GitRef: "main"}, eng, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, time.Minute, ref.Key(), eng))
	return ref.Key()
}
func TestHookSecretBoundariesAndReplay(t *testing.T) {
	type input struct {
		Token  string `json:"token" secret:"true"`
		Direct string `json:"direct"`
	}
	op := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "hook_secret_probe"}, func(_ coreops.OpDependencies, ctx context.Context, in input) (map[string]any, error) {
		if in.Direct != "" {
			_, err := permissions.FromContext(ctx).Resolve(ctx, []string{in.Direct})
			if err != nil {
				return nil, err
			}
		}
		if in.Token != "" && !strings.HasSuffix(in.Token, "-private-value\n") {
			return nil, fmt.Errorf("secret not materialized")
		}
		return map[string]any{"ok": true}, nil
	})
	ws := workspaceTestWorker(t, op)
	for _, mode := range []string{"allowed", "no_grant", "other_hook", "empty_override", "provider_denied", "wrong_commit", "call_limit", "caller_isolated"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			name := "token"
			if mode == "provider_denied" {
				name = "missing"
			}
			hook := ".c2/hooks/check.yaml"
			head := commitHookFile(t, repo, hook, fmt.Sprintf("id: check\nsequence:\n - id: input\n   op: hook_secret_probe\n   inputs: {token: '${{ context.secrets.%s }}'}\n - id: direct\n   op: hook_secret_probe\n   inputs: {direct: %s}\noutputs: {ok: '{{ sequence.direct.outputs.ok }}'}\n", name, name))
			ctx, eng, rt, provider := hookTestEngine(t, ws)
			entry := hookpolicy.Entry{Perms: permissions.Spec{JobDB: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{name}}}}, Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{name: {Mode: "candidate"}}}}
			if mode == "wrong_commit" {
				entry.Trust.Secrets[name] = hookpolicy.Trust{Mode: "commit", Commit: strings.Repeat("a", 40)}
			}
			key := hook
			if mode == "other_hook" {
				key = ".c2/hooks/other.yaml"
			}
			doc := hookpolicy.Document{Version: 1, Hooks: map[string]hookpolicy.Entry{key: entry}}
			if mode != "no_grant" {
				raw, err := json.Marshal(doc)
				require.NoError(t, err)
				_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.TenantKey, Value: raw})
				require.NoError(t, err)
			}
			if mode == "empty_override" {
				id, err := hookpolicy.Identity(repo)
				require.NoError(t, err)
				raw, _ := json.Marshal(hookpolicy.Document{Version: 1, Repository: id, Hooks: map[string]hookpolicy.Entry{hook: hookpolicy.Empty()}})
				_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.CellKey(id), Value: raw})
				require.NoError(t, err)
			}
			body := "id: parent\nsequence:\n - id: hook\n   include: cell://" + hook + "\n"
			if mode == "call_limit" {
				body += "   perms: {limit: {jobdb: {}}}\n"
			}
			if mode == "caller_isolated" {
				body += " - id: caller\n   op: hook_secret_probe\n   inputs: {direct: token}\n"
			}
			job := startHookTest(t, ctx, eng, repo, body)
			_, err := swfutil.JobResult(ctx, eng, job)
			if mode != "allowed" {
				require.Error(t, err)
				if mode != "provider_denied" && mode != "caller_isolated" {
					require.Empty(t, provider.Calls())
				}
				return
			}
			require.NoError(t, err)
			require.Len(t, provider.Calls(), 2)
			run, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: job, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			raw, err := json.Marshal(run)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-value")
			require.Contains(t, string(raw), head)
			_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.TenantKey, Value: json.RawMessage(`{"broken":true}`)})
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(repo))
			_, err = eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: job, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			require.Len(t, provider.Calls(), 2)
		})
	}
}

func TestHookBrokenSourcesAndOutputFailClosed(t *testing.T) {
	ws := workspaceTestWorker(t)
	for _, mode := range []string{"malformed", "symlink", "contract", "missing", "invalid_schema"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			body := "id: check\nsequence: []\noutputs: {decision: bad}\n"
			if mode == "malformed" {
				body = "sequence: ["
			}
			if mode != "missing" {
				commitHookFile(t, repo, ".c2/hooks/check.yaml", body)
			}
			if mode == "symlink" {
				require.NoError(t, os.Remove(filepath.Join(repo, ".c2/hooks/check.yaml")))
				require.NoError(t, os.Symlink("absent.yaml", filepath.Join(repo, ".c2/hooks/check.yaml")))
				require.NoError(t, runGit(repo, "git", "add", "."))
				require.NoError(t, runGit(repo, "git", "commit", "-m", "link"))
			}
			ctx, eng, _, _ := hookTestEngine(t, ws)
			schema := "{type: object, properties: {decision: {enum: [continue]}}}"
			if mode == "invalid_schema" {
				schema = "{type: invalid}"
			}
			fallback := "     if_missing: {outputs: {decision: continue}}\n"
			if mode == "missing" {
				fallback = ""
			}
			job := startHookTest(t, ctx, eng, repo, "id: parent\nstate:\n initial: hook\n states:\n  hook:\n   include:\n     recipe: cell://.c2/hooks/check.yaml\n"+fallback+"   expect_outputs: "+schema+"\n   transitions: [{to: after}]\n  after:\n   op: workspace_probe\n")
			_, err := swfutil.JobResult(ctx, eng, job)
			require.Error(t, err)
			run, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: job, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			raw, _ := json.Marshal(run)
			require.NotContains(t, string(raw), `"task_type":"workspace_probe`)
		})
	}
}

type hookChildSubmitter struct{ access *permissions.Access }

func (s hookChildSubmitter) SubmitJobRef(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	return s.access.Submit(ctx, job)
}

func TestHookChildJobsCannotBroadenAuthority(t *testing.T) {
	for _, mode := range []string{"allowed", "forged", "commit_bound"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			writeHookFunctionPackage(t, repo)
			head := commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\nsequence:\n - id: child\n   op: hook_submit_child\n")
			children := make(chan jobdb.JobKey, 1)
			name := "token"
			if mode == "forged" {
				name = "stolen"
			}
			submit := coreops.NewActivityMappedOpV2[map[string]any, map[string]any](coreops.OpMetadata{Type: "hook_submit_child"}, func(deps coreops.OpDependencies, ctx context.Context, _ map[string]any) (map[string]any, error) {
				rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf("id: child\nextensions:\n functions:\n  - selector: git+file://%s//tools/cel@main\nsequence:\n - id: read\n   op: hook_child_secret\n   inputs: {name: \"${{ hook_lower('%s') }}\"}\n", repo, strings.ToUpper(name))))
				if err != nil {
					return nil, err
				}
				wide := permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"stolen", "token"}}, Jobs: permissions.JobPermissions{Submit: true}}
				start := workflowctl.StartJob{TenantId: deps.JobTool().GetJobKey().TenantId, RecipeName: "child", GitRef: "main", JobContext: contextual.JobContext{InheritedPermissions: &permissions.State{Version: 1, Grant: &wide, Limit: &wide}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}
				ref, err := starter.StartRecipeJob(ctx, start, hookChildSubmitter{permissions.FromContext(ctx)}, *rec)
				if err != nil {
					return nil, err
				}
				children <- ref.Key()
				return map[string]any{}, nil
			})
			probe := coreops.NewActivityMappedOpV2[map[string]string, map[string]any](coreops.OpMetadata{Type: "hook_child_secret"}, func(_ coreops.OpDependencies, ctx context.Context, in map[string]string) (map[string]any, error) {
				_, err := permissions.FromContext(ctx).Resolve(ctx, []string{in["name"]})
				return map[string]any{}, err
			})
			ws := workspaceTestWorker(t, submit, probe)
			ctx, eng, rt, provider := hookTestEngine(t, ws)
			trust := hookpolicy.Trust{Mode: "candidate"}
			if mode == "commit_bound" {
				trust = hookpolicy.Trust{Mode: "commit", Commit: head}
			}
			doc := hookpolicy.Document{Version: 1, Hooks: map[string]hookpolicy.Entry{".c2/hooks/check.yaml": {Perms: permissions.Spec{JobDB: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"token"}}, Jobs: permissions.JobPermissions{Submit: true}}}, Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{"token": trust}, Submit: &hookpolicy.Trust{Mode: "candidate"}}}}}
			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.TenantKey, Value: raw})
			require.NoError(t, err)
			parent := startHookTest(t, ctx, eng, repo, "id: parent\nsequence:\n - id: hook\n   include: cell://.c2/hooks/check.yaml\n")
			_, err = swfutil.JobResult(ctx, eng, parent)
			require.NoError(t, err)
			var child jobdb.JobKey
			select {
			case child = <-children:
			default:
				t.Fatal("child not submitted")
			}
			require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, time.Minute, child, eng))
			_, err = swfutil.JobResult(ctx, eng, child)
			if mode == "allowed" {
				require.NoError(t, err)
				require.Equal(t, [][]string{{"token"}}, provider.Calls())
			} else {
				require.Error(t, err)
				require.Empty(t, provider.Calls())
			}
		})
	}
}
