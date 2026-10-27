package executionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

type inputAuthorityIssuer struct {
	t     *testing.T
	calls int
}

func (i *inputAuthorityIssuer) IssueRestrictedLease(ctx context.Context, task permissions.TaskDelegator, grant permissions.JobDB, deadline time.Time) (jobdb.ExecutionLease, error) {
	i.calls++
	require.Equal(i.t, []string{"bar"}, grant.Secrets.Resolve)
	lease, err := (permissions.JobDBIssuer{}).IssueRestrictedLease(ctx, task, grant, deadline)
	if err != nil {
		return nil, err
	}
	denied, err := lease.ResolveSecrets(ctx, []string{"foo"})
	require.NoError(i.t, err)
	require.Empty(i.t, denied.Secrets, "the real delegated token must not include the input secret")
	return lease, nil
}

func TestRecipeInputAuthorityAndRemoteLeaseIsolation(t *testing.T) {
	previous := coreops.List()
	t.Cleanup(func() { coreops.Replace(previous...) })
	type input struct {
		Token string `json:"token" secret:"true"`
		Bar   bool   `json:"bar"`
	}
	probe := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "input_authority_probe"}, func(_ coreops.OpDependencies, ctx context.Context, in input) (map[string]any, error) {
		if !strings.HasPrefix(in.Token, "credential-foo-") {
			return nil, fmt.Errorf("input was not materialized")
		}
		access := permissions.FromContext(ctx)
		if err := access.CheckSecrets([]string{"foo"}); err == nil {
			return nil, fmt.Errorf("input secret leaked into operation authority")
		}
		for _, entry := range gitprofile.Env(ctx, nil) {
			if socket, ok := strings.CutPrefix(entry, gitprofile.SocketEnv+"="); ok {
				if _, err := gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "secret", Name: "foo"}); err == nil {
					return nil, fmt.Errorf("input secret leaked into helper")
				}
			}
		}
		if in.Bar {
			if _, err := access.Resolve(ctx, []string{"bar"}); err != nil {
				return nil, err
			}
		} else if err := access.CheckSecrets([]string{"bar"}); err == nil {
			return nil, fmt.Errorf("include grant leaked into descendant")
		}
		return map[string]any{"ok": true}, nil
	})
	coreops.Replace(probe)
	for _, tc := range []struct {
		name, rootPerms, includePerms, childPerms, token string
		include, foreign, bar, deny                      bool
	}{
		{name: "root input with unrelated grant", rootPerms: "perms: {jobdb: {secrets: {resolve: [bar]}}}", bar: true},
		{name: "root input with deny-all lease", rootPerms: "perms: {jobdb: {}}"},
		{name: "same repo include", include: true},
		{name: "foreign direct denied", include: true, foreign: true, includePerms: "perms: {limit: {jobdb: {}}}", deny: true},
		{name: "foreign include input authority", include: true, foreign: true, includePerms: "perms: {jobdb: {secrets: {resolve: [foo, bar]}}}"},
		{name: "foreign node explicit lease", include: true, foreign: true, includePerms: "perms: {jobdb: {secrets: {resolve: [foo, bar]}}}", childPerms: "perms: {jobdb: {secrets: {resolve: [bar]}}}", bar: true},
		{name: "forwarded input only", include: true, foreign: true, includePerms: "perms: {limit: {jobdb: {}}}", token: "${{ inputs.token }}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			for _, args := range [][]string{{"init", "-b", "main", root}, {"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial"}, {"-C", root, "config", "user.name", "Test"}, {"-C", root, "config", "user.email", "test@example.com"}} {
				out, err := exec.Command("git", args...).CombinedOutput()
				require.NoError(t, err, string(out))
			}
			childRoot := root
			if tc.foreign {
				childRoot = t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(childRoot, ".git"), 0700))
			}
			token := tc.token
			if token == "" {
				token = "${{ context.secrets.foo }}"
			}
			body := fmt.Sprintf("op: input_authority_probe\n%s\ninputs: {token: '%s', bar: %t}\n", tc.childPerms, token, tc.bar)
			source := "id: root\n" + tc.rootPerms + "\n" + body
			if tc.include {
				childBody := "sequence:\n- id: probe\n  " + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n  ") + "\n"
				child := "id: child\n" + childBody
				if tc.token != "" {
					child = "id: child\ninput_schema: {token: {type: string, secret: true}}\ninputs: {token: '${{ inputs.token }}'}\n" + childBody
				}
				childPath := filepath.Join(childRoot, "child.yaml")
				require.NoError(t, os.WriteFile(childPath, []byte(child), 0600))
				source = fmt.Sprintf("id: root\nsequence:\n- include: %q\n  %s\n", childPath, tc.includePerms)
				if tc.token != "" {
					source += "  inputs: {token: '${{ context.secrets.foo }}'}\n"
				}
			}
			rootFile := filepath.Join(root, "recipe.yaml")
			require.NoError(t, os.WriteFile(rootFile, []byte(source), 0600))
			rec, err := recipe.LoadRecipeFromString([]byte(source))
			require.NoError(t, err)
			expanded, err := compiler.ResolveInlineRecipes(ctx, *rec, compiler.InlineResolutionOptions{RootFile: rootFile})
			require.NoError(t, err)
			provider := &freshGitProvider{}
			base := toy.New(toy.WithSecretProvider(provider))
			server := httptest.NewServer(remote.NewServer(base))
			defer server.Close()
			client, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(client).BuildEngine()
			require.NoError(t, err)
			start := workflowctl.StartJob{TenantId: "test", RecipeName: "root", JobContext: contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}, GitBase: contextual.GitBaseContext{BaseRepo: root, BaseRef: "main"}}}
			ref, err := starter.StartRecipeJob(ctx, start, jobdbschema.WorkflowEngine{Engine: engine, Registry: client}, expanded.Recipe)
			require.NoError(t, err)
			rt := New(client, execution.Allocation{SchemaVersion: 1})
			issuer := &inputAuthorityIssuer{t: t}
			rt.Issuer = issuer
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			ws, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{WrapTaskWorker: rt.WrapTaskWorker})
			require.NoError(t, err)
			engine, err = jobworkflow.NewEngineBuilder().WithRuntime(client).WithWorkerTenantId("test").PlusWorkers(ws.JobWorker).BuildEngine()
			require.NoError(t, err)
			var workers []jobworkflow.TaskWorker
			for _, w := range ws.TaskWorkers {
				workers = append(workers, w)
			}
			run, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: "input-trust", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, err := run.Run(nil)
			require.NoError(t, err)
			if tc.deny {
				require.ErrorContains(t, out.JobError, "lease permission denied")
				require.Zero(t, provider.calls.Load())
				return
			}
			require.NoError(t, out.JobError)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status)
			if tc.bar {
				require.Positive(t, issuer.calls)
			} else {
				require.Zero(t, issuer.calls, "input preparation must not mint a delegated credential")
			}
			history, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: ref.Key(), IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			raw, err := json.Marshal(history)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "credential-foo-")
			calls := provider.calls.Load()
			_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: ref.Key(), JobWorker: compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			require.Equal(t, calls, provider.calls.Load())
		})
	}
}
