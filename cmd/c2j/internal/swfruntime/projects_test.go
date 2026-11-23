package swfruntime

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/localruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/projectcatalog"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestOpenEmbedProjectAdmission(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	handle, err := Open(ctx, "embed:///")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, handle.Cleanup()) })
	root, err := resolveEmbedRoot()
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(root, "swf.db"))
	require.NoError(t, err)
	defer db.Close()
	store, err := projectcatalog.NewSQLStore(db, projectcatalog.SQLite)
	require.NoError(t, err)
	catalog := &projectcatalog.Service{Store: store}
	_, err = catalog.ListProjects(ctx, jobdb.ListProjectsRequest{})
	require.ErrorIs(t, err, jobdb.ErrProjectAccessDenied)
	projects, err := catalog.ListProjects(localruntime.Context(ctx), jobdb.ListProjectsRequest{})
	require.NoError(t, err)
	var ids []string
	for _, project := range projects.Projects {
		ids = append(ids, project.ID)
	}
	require.ElementsMatch(t, []string{"0", "c2"}, ids)
	_, err = handle.Runtime.CreateCell(ctx, jobdb.CreateCellRequest{TenantID: "unregistered", Name: "test", Repo: "example.test/test"})
	require.ErrorIs(t, err, jobdb.ErrProjectNotFound)
	projects, err = catalog.ListProjects(localruntime.Context(ctx), jobdb.ListProjectsRequest{})
	require.NoError(t, err)
	require.Len(t, projects.Projects, 2, "resource writes must not create project identity")
}

func TestOpenEmbedFencesUninstalledProjects(t *testing.T) {
	for _, fixture := range []string{"empty-database", "current-uninstalled", "orphaned-provider", "unknown-file"} {
		t.Run(fixture, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			ctx := context.Background()
			root, err := resolveEmbedRoot()
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(root, 0700))
			path := filepath.Join(root, "swf.db")
			switch fixture {
			case "empty-database", "current-uninstalled":
				require.NoError(t, os.WriteFile(path, nil, 0600))
				if fixture == "current-uninstalled" {
					db, err := sql.Open("sqlite", path)
					require.NoError(t, err)
					require.NoError(t, sqliteruntime.InitializeProjectStorage(ctx, db))
					require.NoError(t, db.Close())
				}
			case "orphaned-provider":
				require.NoError(t, os.Mkdir(filepath.Join(root, "swf.db.blobs"), 0700))
			case "unknown-file":
				require.NoError(t, os.WriteFile(filepath.Join(root, "backup"), []byte("owner state"), 0600))
			}
			before, beforeErr := os.ReadFile(path)
			// Failure releases the owner lock but never silently retries installation.
			for i := 0; i < 2; i++ {
				handle, err := Open(ctx, "embed:///")
				require.Nil(t, handle)
				require.ErrorIs(t, err, jobdb.ErrProjectUnavailable)
				require.Contains(t, err.Error(), "explicit")
			}
			after, afterErr := os.ReadFile(path)
			if beforeErr == nil {
				require.NoError(t, afterErr)
				require.Equal(t, before, after, "opening must not migrate or install existing state")
			} else {
				require.ErrorIs(t, afterErr, os.ErrNotExist)
			}
			_, err = os.Stat(path + ".secrets.db")
			require.ErrorIs(t, err, os.ErrNotExist, "admission must precede provider startup")
		})
	}
}
