package cmd

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func TestCellCreateAndList(t *testing.T) {
	rt := toy.New()
	server := httptest.NewServer(celltest.Handler(remote.NewServer(rt)))
	defer server.Close()
	run := func(args ...string) ([]byte, error) {
		cmd := newCellCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append(args, "--jobdb", server.URL+"/tenant", "--json"))
		err := cmd.Execute()
		return out.Bytes(), err
	}
	for _, name := range []string{"root", "payments"} {
		raw, err := run("create", name, "--repo", "git@EXAMPLE.com:team/"+name+".git")
		require.NoError(t, err)
		var cell jobdb.Cell
		require.NoError(t, json.Unmarshal(raw, &cell))
		require.NotEmpty(t, cell.ID)
		require.Equal(t, name, cell.Name)
		require.Equal(t, "example.com/team/"+name, cell.Repo)
	}
	raw, err := run("list", "--all")
	require.NoError(t, err)
	var cells []jobdb.Cell
	require.NoError(t, json.Unmarshal(raw, &cells))
	require.Len(t, cells, 2)
	_, err = run("create", "root", "--repo", "example.com/other")
	require.Error(t, err)
	_, err = run("create", "../bad", "--repo", "example.com/other")
	require.Error(t, err)
}
func TestCellListDependencies(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".c2"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2/config.yaml"), []byte("self:\n  cell: app\npattern: 'example.com/${{ cell }}'\ndependents:\n - example.com/payments\n"), 0644))
	t.Chdir(root)
	cmd := newCellCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--dependencies", "--json"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "payments")
}
