package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/hookpolicy"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func grantHookTestSecret(t *testing.T, ctx context.Context, rt *toyruntime.Runtime, hook string) {
	t.Helper()
	doc := hookpolicy.Document{Version: 1, Hooks: map[string]hookpolicy.Entry{hook: {
		Perms: permissions.Spec{JobDB: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"token"}}}},
		Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{"token": {Mode: "candidate"}}},
	}}}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.TenantKey, Value: raw})
	require.NoError(t, err)
}

func writeHookFunctionPackage(t *testing.T, repo string) {
	t.Helper()
	writeFunctionPackage(t, filepath.Join(repo, "tools", "cel"), `name: hook_text
version: 1.0.0
functions:
 - name: hook_lower
   mode: function
   execution: sh lower.sh
   args:
    - name: text
      schema: {type: string}
   returns:
     schema: {type: string}
`, map[string]string{"lower.sh": "tr '[:upper:]' '[:lower:]'"})
}

func TestHookFloatingDependenciesWithSecrets(t *testing.T) {
	type secretInput struct {
		Token string `json:"token" secret:"true"`
	}
	probe := coreops.NewActivityMappedOpV2[secretInput, map[string]any](coreops.OpMetadata{Type: "hook_import_secret"}, func(_ coreops.OpDependencies, _ context.Context, in secretInput) (map[string]any, error) {
		if in.Token != "token-private-value\n" {
			return nil, fmt.Errorf("secret not materialized")
		}
		return map[string]any{"ok": true}, nil
	})
	ws := workspaceTestWorker(t, probe, extops.GetExecutionOp())
	for _, mode := range []string{"same_repo", "foreign_granted", "foreign_denied", "external_op"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			foreign := filepath.Join(t.TempDir(), "foreign")
			workspaceRepo(t, repo, "A")
			workspaceRepo(t, foreign, "B")
			leaf := "id: leaf\nsequence:\n - id: secret\n   op: hook_import_secret\n   inputs: {token: '${{ context.secrets.token }}'}\noutputs: {ok: '{{ sequence.secret.outputs.ok }}'}\n"
			commitHookFile(t, repo, ".c2/hooks/leaf.yaml", leaf)
			commitHookFile(t, foreign, "leaf.yaml", leaf)
			commitHookFile(t, foreign, "recipe.yaml", "id: foreign\nsequence:\n - id: leaf\n   include: ./leaf.yaml\noutputs: {ok: '{{ sequence.leaf.outputs.ok }}'}\n")
			commitHookFile(t, foreign, "op/op.yaml", `name: hook_secret_extension
input_schema:
 type: object
 required: [token]
 properties:
  token: {type: string, x-secret: true}
output_schema:
 type: object
 properties:
  ok: {type: boolean}
run: |
 IFS= read -r payload
 case "$payload" in *token-private-value*) ;; *) exit 9 ;; esac
 printf '{"output":{"ok":true}}'
`)
			node := "   include: ./leaf.yaml\n"
			if mode == "foreign_granted" || mode == "foreign_denied" {
				node = fmt.Sprintf("   include: git+file://%s//recipe.yaml@main\n", foreign)
				if mode == "foreign_granted" {
					node += "   perms: {jobdb: {secrets: {resolve: [token]}}}\n"
				}
			}
			if mode == "external_op" {
				node = fmt.Sprintf("   op: git+file://%s//op@main\n   inputs: {token: '${{ context.secrets.token }}'}\n", foreign)
			}
			hook := ".c2/hooks/check.yaml"
			commitHookFile(t, repo, hook, "id: hook\nsequence:\n - id: dependency\n"+node+"outputs: {ok: '{{ sequence.dependency.outputs.ok }}'}\n")
			ctx, eng, rt, provider := hookTestEngine(t, ws)
			grantHookTestSecret(t, ctx, rt, hook)
			key := startHookTest(t, ctx, eng, repo, "id: parent\nsequence:\n - id: hook\n   include: cell://"+hook+"\noutputs: {ok: '{{ sequence.hook.outputs.ok }}'}\n")
			result, err := swfutil.JobResult(ctx, eng, key)
			if mode == "foreign_denied" {
				require.Error(t, err)
				require.Empty(t, provider.Calls())
				return
			}
			require.NoError(t, err)
			raw, err := result.GetData()
			require.NoError(t, err)
			require.JSONEq(t, `{"ok":true}`, string(raw))
			require.Equal(t, [][]string{{"token"}}, provider.Calls())
		})
	}
}

func TestHookCELImports(t *testing.T) {
	ws := workspaceTestWorker(t)
	for _, mode := range []string{"local", "external", "nested"} {
		for _, parentKind := range []string{"sequence", "state"} {
			t.Run(mode+"/"+parentKind, func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				foreign := filepath.Join(t.TempDir(), "foreign")
				workspaceRepo(t, repo, "A")
				workspaceRepo(t, foreign, "B")
				writeHookFunctionPackage(t, repo)
				writeHookFunctionPackage(t, foreign)
				commitHookFile(t, foreign, "README", "functions")
				selector := "./tools/cel"
				if mode == "external" {
					selector = fmt.Sprintf("git+file://%s//tools/cel@main", foreign)
				}
				hook := ".c2/hooks/check.yaml"
				body := fmt.Sprintf(`id: hook_body
extensions:
 functions:
  - selector: %s
vars: {text: "${{ hook_lower(caller_lower('HELLO')) }}"}
state:
 initial: test
 states:
  test:
   sequence: []
   transitions:
    - {to: done, when: "hook_lower('GO') == 'go'"}
  done:
   sequence: []
outputs: {value: "${{ hook_lower(vars.text) }}"}
`, selector)
				if mode == "nested" {
					commitHookFile(t, repo, ".c2/hooks/leaf.yaml", body)
					body = "id: hook_body\nsequence:\n - id: leaf\n   include: ./leaf.yaml\noutputs: {value: '{{ sequence.leaf.outputs.value }}'}\n"
				}
				commitHookFile(t, repo, hook, body)
				ctx, eng, rt, _ := hookTestEngine(t, ws)
				// The presence of a secret grant must not change CEL import support.
				grantHookTestSecret(t, ctx, rt, hook)
				parent := "id: parent\nsequence:\n - id: hook\n   include: cell://" + hook + "\n - id: again\n   include: cell://" + hook + "\noutputs: {value: '{{ sequence.again.outputs.value }}'}\n"
				if parentKind == "state" {
					parent = "id: parent\nstate:\n initial: hook\n states:\n  hook:\n   include: cell://" + hook + "\noutputs: {value: '{{ states.hook.outputs.value }}'}\n"
				}
				parent = strings.Replace(parent, "id: parent\n", fmt.Sprintf("id: parent\nextensions:\n functions:\n  - selector: git+file://%s//tools/cel@main\n    rename: {hook_lower: caller_lower}\n", foreign), 1)
				key := startHookTest(t, ctx, eng, repo, parent)
				result, err := swfutil.JobResult(ctx, eng, key)
				require.NoError(t, err)
				raw, err := result.GetData()
				require.NoError(t, err)
				require.JSONEq(t, `{"value":"hello"}`, string(raw))
				// Replay must use the recorded CEL import ref even if its branch moves.
				commitHookFile(t, foreign, "tools/cel/lower.sh", "printf changed")
				replay, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				replayRaw, err := replay.GetData()
				require.NoError(t, err)
				require.JSONEq(t, string(raw), string(replayRaw))
			})
		}
	}
}
