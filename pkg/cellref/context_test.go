package cellref

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestRoutingAssertions(t *testing.T) {
	c := Context{Pattern: "https://example.com/cell-${{ cell }}.git", SelfCell: "A", SelfRepo: "example.com/cell-A", SelfRef: "dev", RootRepo: "example.com/root", RootRef: "trunk"}
	for _, tt := range []struct{ name, repo, ref string }{{"A", "example.com/cell-A", "dev"}, {"B", "example.com/cell-B", "main"}, {"root", "example.com/root", "trunk"}} {
		got, err := c.Resolve(tt.name, "")
		require.NoError(t, err)
		require.Equal(t, tt.repo, got.Repository)
		require.Equal(t, tt.ref, got.Ref)
	}
	target, err := (Context{}).Resolve("payments", "")
	require.NoError(t, err)
	require.Empty(t, target.Repository)
	for _, name := range []string{"./repo", "https://example.com/repo", ".", ".."} {
		_, err := (Context{}).Resolve(name, "")
		require.Error(t, err)
	}
}
func TestMissingCellRequiresAssertionAndNeverBootstrapsRoot(t *testing.T) {
	missing := func(context.Context, string) (jobdb.ResolvedCell, error) {
		return jobdb.ResolvedCell{}, jobdb.ErrCellNotFound
	}
	cell, err := ResolveTarget(celltest.Context(context.Background()), missing, "payments", "git@example.com:team/payments.git")
	require.NoError(t, err)
	require.Empty(t, cell.CellID)
	require.Equal(t, "example.com/team/payments", cell.CellRepo)
	_, err = ResolveTarget(celltest.Context(context.Background()), missing, "payments", "")
	require.ErrorIs(t, err, jobdb.ErrCellNotFound)
	_, err = ResolveTarget(celltest.Context(context.Background()), missing, "root", "example.com/root")
	require.ErrorIs(t, err, jobdb.ErrCellNotFound)
	denied := func(context.Context, string) (jobdb.ResolvedCell, error) {
		return jobdb.ResolvedCell{}, jobdb.ErrCellForbidden
	}
	_, err = ResolveTarget(celltest.Context(context.Background()), denied, "payments", "example.com/payments")
	require.ErrorIs(t, err, jobdb.ErrCellForbidden)
}
