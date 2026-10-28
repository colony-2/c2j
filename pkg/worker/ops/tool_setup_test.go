package ops

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestToolSetupFailureDiagnostics(t *testing.T) {
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	result := PrepareTools(context.Background(), ToolSetupRequest{Scopes: []toolenv.Scope{{ID: "recipe", Packages: []string{"unsupported:tool"}}}})
	require.Contains(t, result.Diagnostics.Error, "unsupported package prefix")
	require.Len(t, result.Diagnostics.Tools, 1)
	require.Equal(t, "failed", result.Diagnostics.Tools[0].Outcome)
}
func TestExtensionResolutionFailure(t *testing.T) {
	inv := ActivityInvocationRequest{Preparation: PreparationRequest{Extension: &ExtensionResolutionRequest{Selector: "./missing", Options: extops.ResolveOptions{BaseDir: t.TempDir()}}}}
	_, err := ResolveInvocation(context.Background(), &inv)
	require.ErrorContains(t, err, "extension setup failed")
}

func TestPreparationFailureAttachesOutcomeDiagnostics(t *testing.T) {
	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()
	inv := ActivityInvocationRequest{Input: map[string]any{}, Preparation: PreparationRequest{Scopes: []toolenv.Scope{{ID: "test", Packages: []string{"unsupported:tool"}}}}}
	inv.GitTaskContext = gitstate.GlobalGitTaskContext{BaseRepo: baseRepo, BaseRef: baseHash}
	worker := taskWorker{doer: &opExecutor{deps: coreops.NewServiceDepsBuilder().Build(), controller: gitstate.NewController(nil)}}
	tc := jobworkflow.NewTaskContext(jobdb.JobKey{TenantId: "test", JobId: "prep"}, 7, nil, nil, nil)
	data, err := worker.RunTask(deniedTaskContext(t, tc), tc, jobdb.NewTaskDataOrPanic(inv))
	require.ErrorContains(t, err, "unsupported package prefix")
	require.NotNil(t, data)
	artifacts, err := data.GetArtifacts()
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	raw, err := artifacts[0].Bytes(context.Background())
	require.NoError(t, err)
	var diagnostic ActivityInvocationOutput
	require.NoError(t, json.Unmarshal(raw, &diagnostic))
	require.NotNil(t, diagnostic.Setup)
	require.EqualValues(t, 7, *diagnostic.Setup.TaskOrdinal)
	require.Contains(t, diagnostic.Setup.Error, "unsupported package prefix")
}

func TestPreparationAttachesExtensionSecretDefaults(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "op.yaml"), []byte(`name: fallback
input_schema:
  type: object
  properties:
    token: {type: string, x-secret: true, default: schema}
    omitted: {type: string, x-secret: true, default: ""}
    binding: {type: string, x-secret: true, default: '${{ context.secrets.bound }}'}
    explicit: {type: string, x-secret: true, default: schema}
output_schema: {type: object}
run: unused
`), 0600))
	rc, err := template.NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{})
	require.NoError(t, err)
	snapshot, err := rc.InputSnapshot()
	require.NoError(t, err)
	raw := map[string]any{
		"token":    "${{ context.secrets.missing }}",
		"explicit": `${{ context.secrets.?missing.orValue("explicit") }}`,
	}
	input, err := rc.ResolveInputs(raw)
	require.NoError(t, err)
	inv := ActivityInvocationRequest{Permissions: &permissions.State{Version: permissions.Version, Grant: &permissions.JobDB{Secrets: permissions.SecretPermissions{Resolve: []string{"bound"}}}}, Input: map[string]any{"inputs": input}, Preparation: PreparationRequest{Extension: &ExtensionResolutionRequest{
		Selector: "./", Options: extops.ResolveOptions{BaseDir: root}, RawInputs: raw, TemplateContext: snapshot,
	}}}
	require.NoError(t, PrepareInvocation(context.Background(), &inv, func(_ context.Context, req ToolSetupRequest) ToolSetupResult {
		return ToolSetupResult{Extension: req.Extension}
	}))
	refs, err := secrets.InvocationReferences(reflect.TypeFor[extops.ExecutionInput](), inv.Input)
	require.NoError(t, err)
	session := secrets.NewSession()
	defer session.Close()
	calls := 0
	require.NoError(t, session.PrepareReferences(context.Background(), refs, func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		calls++
		require.ElementsMatch(t, []string{"missing", "bound"}, names)
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"bound": {Value: "resolved-value"}}}, nil
	}))
	require.Equal(t, 1, calls)
	value, err := session.Materialize(inv.Input["inputs"])
	require.NoError(t, err)
	require.Equal(t, map[string]any{"token": "schema", "omitted": "", "explicit": "explicit", "binding": "resolved-value"}, value)
}
