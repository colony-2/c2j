package recipejob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, root, raw string) {
	t.Helper()
	path := filepath.Join(root, ".c2", "config.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(strings.TrimSpace(raw)+"\n"), 0644))
}
func TestResolveNamedTargets(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "self:\n  cell: payments\n  repo: github.com/acme/payments\n  ref: release\n")
	resolve := func(context.Context, string) (jobdb.ResolvedCell, error) {
		return jobdb.ResolvedCell{Cell: "payments", CellID: "server-id", CellRepo: "github.com/acme/payments"}, nil
	}
	target, err := ResolveTarget(celltest.Context(context.Background()), ResolveTargetRequest{WorkingDir: dir, Self: true, ResolveCell: resolve})
	require.NoError(t, err)
	require.Equal(t, "server-id", target.CellID)
	require.Equal(t, "payments", target.CellName)
	require.Equal(t, "release", target.DefaultRef)
	_, err = ResolveTarget(celltest.Context(context.Background()), ResolveTargetRequest{WorkingDir: dir, Cell: "payments", CellRepo: "github.com/acme/other", ResolveCell: resolve})
	require.ErrorIs(t, err, jobdb.ErrCellRepoMismatch)
	for _, name := range []string{"github.com/acme/payments", "./repo", "/tmp/repo", ".", ".."} {
		_, err := ResolveTarget(celltest.Context(context.Background()), ResolveTargetRequest{WorkingDir: dir, Cell: name})
		require.Error(t, err, name)
	}
}
func TestSelfRequiresExplicitName(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "self:\n  repo: github.com/acme/payments\n")
	_, err := ResolveTarget(celltest.Context(context.Background()), ResolveTargetRequest{WorkingDir: dir})
	require.ErrorContains(t, err, "self.cell")
}
func TestNameOnlyTargetUsesRegistry(t *testing.T) {
	target, err := ResolveTarget(celltest.Context(context.Background()), ResolveTargetRequest{WorkingDir: t.TempDir(), Cell: "payments", ResolveCell: func(context.Context, string) (jobdb.ResolvedCell, error) {
		return jobdb.ResolvedCell{Cell: "payments", CellID: "server-id", CellRepo: "github.com/acme/payments"}, nil
	}})
	require.NoError(t, err)
	require.Equal(t, "github.com/acme/payments", target.CellRepo)
}
