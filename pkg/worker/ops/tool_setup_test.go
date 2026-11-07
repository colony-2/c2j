package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestToolSetupFailureDiagnostics(t *testing.T) {
	t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
	result := PrepareTools(celltest.Context(context.Background()), ToolSetupRequest{Scopes: []toolenv.Scope{{ID: "recipe", Packages: []string{"unsupported:tool"}}}})
	require.Contains(t, result.Diagnostics.Error, "unsupported package prefix")
	require.Len(t, result.Diagnostics.Tools, 1)
	require.Equal(t, "failed", result.Diagnostics.Tools[0].Outcome)
}
func TestExtensionResolutionFailure(t *testing.T) {
	inv := ActivityInvocationRequest{Preparation: PreparationRequest{Extension: &ExtensionResolutionRequest{Selector: "./missing", Options: extops.ResolveOptions{BaseDir: t.TempDir()}}}}
	_, err := ResolveInvocation(celltest.Context(context.Background()), &inv)
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
	raw, err := artifacts[0].Bytes(celltest.Context(context.Background()))
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
	require.NoError(t, PrepareInvocation(celltest.Context(context.Background()), &inv, func(_ context.Context, req ToolSetupRequest) ToolSetupResult {
		return ToolSetupResult{Extension: req.Extension}
	}))
	refs, err := secrets.InvocationReferences(reflect.TypeFor[extops.ExecutionInput](), inv.Input)
	require.NoError(t, err)
	session := secrets.NewSession()
	defer session.Close()
	calls := 0
	require.NoError(t, session.PrepareReferences(celltest.Context(context.Background()), refs, func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
		calls++
		require.ElementsMatch(t, []string{"missing", "bound"}, names)
		return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"bound": {Value: "resolved-value"}}}, nil
	}))
	require.Equal(t, 1, calls)
	value, err := session.Materialize(inv.Input["inputs"])
	require.NoError(t, err)
	require.Equal(t, map[string]any{"token": "schema", "omitted": "", "explicit": "explicit", "binding": "resolved-value"}, value)
}

// Exercise the actual live setup chain, including repository scope collection,
// retained manifests and regeneration of a runner from an older worker.
func TestLiveSetupRepairsDeclaredRunner(t *testing.T) {
	root, base, cache := t.TempDir(), t.TempDir(), t.TempDir()
	log := filepath.Join(t.TempDir(), "install.log")
	t.Setenv("C2J_TOOL_CACHE_DIR", cache)
	t.Setenv("RUNNER_INSTALL_LOG", log)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("packages: [uv:manager==repo]\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "uv"), []byte(`#!/bin/sh
set -eu
for spec do :; done
printf '%s\n' "$spec" >> "$RUNNER_INSTALL_LOG"
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\nprintf "%%s\\n" %s\n' "'$spec'" > "$UV_TOOL_BIN_DIR/pnpm"
chmod +x "$UV_TOOL_BIN_DIR/pnpm"
`), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(base, "pnpm"), []byte("#!/bin/sh\necho 12.18.0\n"), 0700))
	t.Setenv("PATH", base+string(os.PathListSeparator)+os.Getenv("PATH"))
	originalPath := os.Getenv("PATH")
	ctx := celltest.Context(context.Background())
	var inv ActivityInvocationRequest
	req := ToolSetupRequest{Scopes: []toolenv.Scope{{ID: "op", Packages: []string{"uv:manager==op"}}}}
	require.NoError(t, InstallWorkspaceInvocation(ctx, &inv, req, root, PrepareTools))
	require.Equal(t, []string{"uv:manager==repo"}, inv.Setup.Diagnostics.Repository.Packages)
	require.Len(t, inv.Setup.Diagnostics.Tools, 2)
	env := *inv.Setup.Environment
	check := func() {
		t.Helper()
		out, stderr, err := process.ExecuteProcess(inv.Setup.Environment.Bind(ctx), process.RunRequest{Command: []string{"pnpm", "--version"}})
		require.NoError(t, err, string(stderr))
		require.Equal(t, "manager==op\n", string(out))
	}
	check()
	before, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "manager==repo\nmanager==op\n", string(before))
	require.NoError(t, os.WriteFile(filepath.Join(env.Path, "pnpm"), []byte("#!/bin/sh\necho 12.18.0\n"), 0700))
	require.NoError(t, InstallWorkspaceInvocation(ctx, &inv, req, root, PrepareTools))
	require.Equal(t, env.Path, inv.Setup.Environment.Path)
	for _, d := range inv.Setup.Diagnostics.Tools {
		require.Equal(t, "reused", d.Outcome)
	}
	check()
	after, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, originalPath, os.Getenv("PATH"))
	// Repository-only declarations also select their executable.
	require.NoError(t, InstallWorkspaceInvocation(ctx, &inv, ToolSetupRequest{}, root, PrepareTools))
	out, stderr, err := process.ExecuteProcess(inv.Setup.Environment.Bind(ctx), process.RunRequest{Command: []string{"pnpm", "--version"}})
	require.NoError(t, err, string(stderr))
	require.Equal(t, "manager==repo\n", string(out))
}
