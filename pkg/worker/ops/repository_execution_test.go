package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/stretchr/testify/require"
)

func TestRepositoryExecutionAdmissionBeforeTools(t *testing.T) {
	for _, cfg := range []string{
		"execution: {base: linux*}", "execution: {capabilities: [docker]}", "execution: {resources: {cpu: 2}}", "execution: {resources: {memory: 8Gi}}", "execution: {resources: {ephemeral-storage: 10Gi}}", "execution: {resources: {memory: wrong}}",
	} {
		t.Run(cfg, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte(cfg+"\npackages: [uv:tool==1]"), 0600))
			inv := ActivityInvocationRequest{}
			err := InstallWorkspaceInvocation(context.Background(), &inv, ToolSetupRequest{}, root, func(context.Context, ToolSetupRequest) ToolSetupResult {
				t.Fatal("tools ran before admission")
				return ToolSetupResult{}
			})
			require.Error(t, err)
			require.NotNil(t, inv.Setup)
			require.NotEmpty(t, inv.Setup.Diagnostics.Repository.Digest)
			require.Equal(t, ".c2/config.yaml", inv.Setup.Diagnostics.Repository.Path)
		})
	}
}
func TestRepositoryExecutionSharedAdmissionAndFreshRead(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	write := func(s string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte(s), 0600))
	}
	write("packages: [uv:tool==1]\nexecution: {base: linux*, resources: {memory: 8Gi}}")
	read := 0
	installed := 0
	ctx := execution.WithRepositoryAdmission(context.Background(), func(ctx context.Context, r execution.Requirements) error {
		read++
		require.Equal(t, []string{"8Gi", "1Gi"}[read-1], *r.Resources.Memory)
		require.Equal(t, "linux*", *r.Base)
		return nil
	})
	for i := 0; i < 2; i++ {
		inv := ActivityInvocationRequest{}
		require.NoError(t, InstallWorkspaceInvocation(ctx, &inv, ToolSetupRequest{}, root, func(context.Context, ToolSetupRequest) ToolSetupResult { installed++; return ToolSetupResult{} }))
		if i == 0 {
			write("packages: [uv:tool==1]\nexecution: {base: linux*, resources: {memory: 1Gi}}")
		}
	}
	require.Equal(t, 2, read)
	require.Equal(t, 2, installed)
	write("packages: [uv:tool==1]\nexecution: {base: '*'}")
	require.NoError(t, InstallWorkspaceInvocation(context.Background(), &ActivityInvocationRequest{}, ToolSetupRequest{}, root, func(context.Context, ToolSetupRequest) ToolSetupResult { return ToolSetupResult{} }))
}

func TestRepositoryExecutionRetainsLegacyConstraintsAndProvenance(t *testing.T) {
	for _, legacy := range []execution.Requirements{
		{Platform: stringPointer("linux/amd64")}, {Image: stringPointer("example.test/runner:v1")},
	} {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("execution: {base: linux*}\npackages: [uv:tool==1]"), 0600))
		inv := ActivityInvocationRequest{Preparation: PreparationRequest{Execution: legacy}}
		inv.GitTaskContext.BaseRepo = "example.test/repo"
		inv.GitTaskContext.ResolvedBaseHash = "restored-commit"
		err := InstallWorkspaceInvocation(context.Background(), &inv, ToolSetupRequest{}, root, func(context.Context, ToolSetupRequest) ToolSetupResult {
			t.Fatal("legacy validation must precede installation")
			return ToolSetupResult{}
		})
		require.ErrorContains(t, err, "legacy")
		diag := inv.Setup.Diagnostics.Repository
		require.Equal(t, ".c2/config.yaml", diag.Path)
		require.NotEmpty(t, diag.Digest)
		require.Equal(t, "repository:root", diag.Scope)
		require.Equal(t, "example.test/repo", diag.Repository)
		require.Equal(t, "restored-commit", diag.Snapshot)
	}
}
func stringPointer(s string) *string { return &s }

// Returning from admission may acknowledge a yield and release the runner.
// Provenance must already be visible at that boundary, without waiting for
// task cleanup or introducing any preparation checkpoint.
func TestRepositoryProvenancePrecedesAdmission(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".c2"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("execution: {base: linux*}\npackages: [uv:tool==1]"), 0600))
	var observed *toolenv.RepositoryDiagnostic
	req := ToolSetupRequest{observeRepository: func(d *toolenv.RepositoryDiagnostic) { observed = d }}
	stop := errors.New("admission yielded")
	ctx := execution.WithRepositoryAdmission(context.Background(), func(context.Context, execution.Requirements) error {
		require.NotNil(t, observed, "provenance must be emitted before admission can yield")
		require.Equal(t, ".c2/config.yaml", observed.Path)
		require.NotEmpty(t, observed.Digest)
		return stop
	})
	inv := ActivityInvocationRequest{}
	err := InstallWorkspaceInvocation(ctx, &inv, req, root, func(context.Context, ToolSetupRequest) ToolSetupResult {
		t.Fatal("installer must not run after rejected admission")
		return ToolSetupResult{}
	})
	require.ErrorIs(t, err, stop)
	require.Same(t, observed, inv.Setup.Diagnostics.Repository)
}
