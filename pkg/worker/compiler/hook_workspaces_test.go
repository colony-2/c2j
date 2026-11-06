package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/jobref"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestHookForeignWorkspacesPreserveSourcePolicyAndSnapshots(t *testing.T) {
	for _, placement := range []string{"include", "container", "body"} {
		t.Run(placement, func(t *testing.T) {
			root := t.TempDir()
			a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
			workspaceRepo(t, a, "A")
			workspaceRepo(t, b, "B")
			hook := ".c2/hooks/check.yaml"
			leaf := ".c2/hooks/leaf.yaml"
			nested := ".c2/hooks/nested.yaml"
			leafYAML := "id: leaf\nsequence: []\noutputs: {marker: %s}\n"
			commitHookFile(t, a, leaf, fmt.Sprintf(leafYAML, "upstream"))
			commitHookFile(t, a, nested, fmt.Sprintf(leafYAML, "upstream"))
			body := `id: check
sequence:
 - id: leaf
   include: ./leaf.yaml
 - id: nested
   include: cell://.c2/hooks/nested.yaml
 - id: access
   op: hook_workspace_access
 - id: write
   op: workspace_probe
   inputs: {read: identity, write: target.txt}
outputs:
 marker: '{{ sequence.leaf.outputs.marker }}'
 nested: '{{ sequence.nested.outputs.marker }}'
 cell: '{{ sequence.write.outputs.cell }}'
 owner: '{{ sequence.write.outputs.owner }}'
 text: '{{ sequence.write.outputs.text }}'
`
			if placement == "body" {
				body = "workspace: {cell: B}\n" + body
			}
			commitHookFile(t, a, hook, body)
			// Foreign files must never become the source of either include form.
			commitHookFile(t, b, hook, "id: wrong\nsequence: []\noutputs: {marker: wrong}\n")
			commitHookFile(t, b, leaf, fmt.Sprintf(leafYAML, "wrong"))
			commitHookFile(t, b, nested, fmt.Sprintf(leafYAML, "wrong"))
			edit := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "hook_workspace_edit"}, func(d coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				for _, path := range []string{leaf, nested} {
					if err := os.WriteFile(filepath.Join(d.WorktreePath(), path), []byte(fmt.Sprintf(leafYAML, "candidate")), 0644); err != nil {
						return nil, err
					}
				}
				return map[string]any{}, nil
			})
			access := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "hook_workspace_access"}, func(_ coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
				_, err := permissions.FromContext(ctx).Resolve(ctx, []string{"token"})
				if err != nil {
					return nil, err
				}
				scope, ok := jobref.FromContext(ctx)
				require.True(t, ok)
				_, err = scope.Submit(ctx, jobdb.SubmitJob{TenantId: "hooks", Cell: "B", CellRepo: celltest.Repo(b), JobType: "child", Data: jobdb.NewTaskDataOrPanic(nil)})
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				return map[string]any{}, nil
			})
			ctx, eng, rt, _ := hookTestEngine(t, workspaceTestWorker(t, edit, access))
			celltest.Create(t, rt, "hooks", "B", celltest.Repo(b))
			id, err := hookpolicy.Identity(celltest.Repo(a))
			require.NoError(t, err)
			doc := hookpolicy.Document{Version: 1, Repository: id, Hooks: map[string]hookpolicy.Entry{hook: {
				Perms: permissions.Spec{JobDB: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"token"}}}},
				Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{"token": {Mode: "candidate"}}},
			}}}
			raw, err := json.Marshal(doc)
			require.NoError(t, err)
			_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.CellKey(id), Value: raw})
			require.NoError(t, err)
			call := " - id: hook\n   include: cell://" + hook + "\n"
			if placement == "include" {
				call += "   workspace: {cell: B}\n"
			}
			if placement == "container" {
				call = ` - id: hook
   workspace: {cell: B}
   sequence:
    - id: check
      include: cell://.c2/hooks/check.yaml
   outputs:
     marker: '{{ sequence.check.outputs.marker }}'
     nested: '{{ sequence.check.outputs.nested }}'
     cell: '{{ sequence.check.outputs.cell }}'
     owner: '{{ sequence.check.outputs.owner }}'
     text: '{{ sequence.check.outputs.text }}'
`
			}
			job := startHookTest(t, ctx, eng, a, `id: foreign-hook
perms: {jobdb: {}}
sequence:
 - id: edit
   op: hook_workspace_edit
 - id: before
   op: workspace_probe
`+call+` - id: after
   op: workspace_probe
   inputs: {absent: target.txt}
outputs:
 hook: '${{ sequence.hook.outputs }}'
 before: '{{ sequence.before.outputs.head }}'
 after: '{{ sequence.after.outputs.head }}'
`)
			result, err := swfutil.JobResult(ctx, eng, job)
			require.NoError(t, err)
			var out map[string]any
			require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &out))
			require.Equal(t, out["before"], out["after"], "foreign writes must not advance the consuming snapshot")
			require.Equal(t, map[string]any{"marker": "candidate", "nested": "candidate", "cell": "B", "owner": "test", "text": "B"}, out["hook"])
			_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.CellKey(id), Value: json.RawMessage(`{"broken":true}`)})
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(a))
			require.NoError(t, os.RemoveAll(b))
			replayed, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: job, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			require.JSONEq(t, string(result.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
		})
	}
}

func TestHookForeignWorkspaceUsesConsumingCommitTrust(t *testing.T) {
	for _, placement := range []string{"include", "recipe"} {
		for _, trust := range []string{"consuming", "target"} {
			t.Run(placement+"/"+trust, func(t *testing.T) {
				root := t.TempDir()
				a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
				workspaceRepo(t, a, "A")
				foreign := workspaceRepo(t, b, "B")
				hook := ".c2/hooks/check.yaml"
				candidate := commitHookFile(t, a, hook, `id: check
sequence:
 - id: access
   op: hook_workspace_token
 - id: write
   op: workspace_probe
   inputs: {read: identity, write: foreign.txt}
outputs: {text: '{{ sequence.write.outputs.text }}'}
`)
				probe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "hook_workspace_token"}, func(_ coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
					_, err := permissions.FromContext(ctx).Resolve(ctx, []string{"token"})
					return map[string]any{}, err
				})
				ctx, eng, rt, _ := hookTestEngine(t, workspaceTestWorker(t, probe))
				celltest.Create(t, rt, "hooks", "B", celltest.Repo(b))
				commit := candidate
				if trust == "target" {
					commit = foreign
				}
				doc := hookpolicy.Document{Version: 1, Hooks: map[string]hookpolicy.Entry{hook: {
					Perms: permissions.Spec{JobDB: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"token"}}}},
					Trust: hookpolicy.SourceTrust{Secrets: map[string]hookpolicy.Trust{"token": {Mode: "commit", Commit: commit}}},
				}}}
				raw, err := json.Marshal(doc)
				require.NoError(t, err)
				_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "hooks", Name: hookpolicy.TenantKey, Value: raw})
				require.NoError(t, err)
				body := `id: foreign-hook
source_hooks: from_base
perms: {jobdb: {}}
sequence:
 - id: hook
   include: cell://.c2/hooks/check.yaml
`
				if placement == "recipe" {
					body = "workspace: {cell: B}\n" + body
				} else {
					body += "   workspace: {cell: B}\n"
				}
				body += "outputs: {text: '{{ sequence.hook.outputs.text }}'}\n"
				job := startHookTest(t, ctx, eng, a, body)
				result, err := swfutil.JobResult(ctx, eng, job)
				if trust == "target" {
					require.ErrorContains(t, err, "not authorized for candidate")
					return
				}
				require.NoError(t, err)
				require.JSONEq(t, `{"text":"B"}`, string(result.GetDataOrPanic()))
				arts, err := result.GetArtifacts()
				require.NoError(t, err)
				require.Nil(t, findThinPack(arts), "foreign hook snapshot must not become the implicit root result")
			})
		}
	}
}
