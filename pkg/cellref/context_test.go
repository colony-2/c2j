package cellref

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortableCellResolution(t *testing.T) {
	ctx := Context{Pattern: "https://example.com/cell-${{ cell }}.git", SelfRepo: "https://example.com/cell-A.git", SelfRef: "dev", RootRepo: "https://example.com/root.git", RootRef: "trunk"}
	for _, tt := range []struct{ cell, ref, repo, wantRef string }{
		{"B", "", "https://example.com/cell-B.git", "main"},
		{"B", "release", "https://example.com/cell-B.git", "release"},
		{"A", "", "https://example.com/cell-A.git", "dev"},
		{"root", "", "https://example.com/root.git", "trunk"},
	} {
		got, err := ctx.Resolve(tt.cell, tt.ref)
		require.NoError(t, err)
		require.Equal(t, tt.repo, got.Repository)
		require.Equal(t, tt.wantRef, got.Ref)
	}
	_, err := (Context{}).Resolve("B", "")
	require.Error(t, err)
	_, err = (Context{}).Resolve("./B", "")
	require.Error(t, err)
}

func TestRelativeCellDefaultsUseCapturedDirectory(t *testing.T) {
	ctx := Context{BaseDir: "/submitter/project", Pattern: "../${{ cell }}", SelfRepo: "../A", SelfRef: "dev", RootRepo: "../root", RootRef: "trunk"}
	for _, tt := range []struct{ cell, ref string }{{"A", "dev"}, {"root", "trunk"}} {
		got, err := ctx.Resolve(tt.cell, "")
		require.NoError(t, err)
		require.Equal(t, "file:///submitter/"+tt.cell, got.Repository)
		require.Equal(t, tt.ref, got.Ref)
	}
}
