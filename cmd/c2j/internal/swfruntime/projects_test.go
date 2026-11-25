package swfruntime

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/jobdbtarget"
	"github.com/colony-2/c2j/internal/localruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/projectcatalog"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestProjectListUninstalledWALLeavesNoSidecars(t *testing.T) {
	root, scratch := setupProjectFixture(t)
	ctx := context.Background()
	path := filepath.Join(root, "swf.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	var mode string
	require.NoError(t, db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode))
	require.Equal(t, "wal", mode)
	require.NoError(t, sqliteruntime.InitializeProjectStorage(ctx, db))
	require.NoError(t, db.Close())
	before := projectFixtureFiles(t, root)
	require.Len(t, before, 2)
	require.Contains(t, before, "swf.db")
	_, err = ListProjects(ctx, jobdbtarget.Target{Embedded: true}, jobdb.ListProjectsRequest{})
	require.ErrorIs(t, err, jobdb.ErrProjectUnavailable)
	require.Equal(t, before, projectFixtureFiles(t, root))
	require.Empty(t, projectFixtureFiles(t, scratch))
}

func TestProjectListRejectsRollbackJournal(t *testing.T) {
	root, scratch := setupProjectFixture(t)
	writerPath := filepath.Join(t.TempDir(), "writer.db")
	db, err := sql.Open("sqlite", writerPath)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("CREATE TABLE prior (name TEXT); INSERT INTO prior VALUES ('committed')")
	require.NoError(t, err)
	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec("UPDATE prior SET name='uncommitted'")
	require.NoError(t, err)
	copyProjectFixture(t, writerPath, filepath.Join(root, "swf.db"))
	copyProjectFixture(t, writerPath+"-journal", filepath.Join(root, "swf.db-journal"))
	before := projectFixtureFiles(t, root)
	_, err = ListProjects(context.Background(), jobdbtarget.Target{Embedded: true}, jobdb.ListProjectsRequest{})
	require.ErrorIs(t, err, jobdb.ErrProjectUnavailable)
	require.ErrorContains(t, err, "rollback journal")
	require.Equal(t, before, projectFixtureFiles(t, root))
	require.Empty(t, projectFixtureFiles(t, scratch))
}

func TestProjectListFindsUncheckpointedWALCommit(t *testing.T) {
	ctx := context.Background()
	root, scratch := setupProjectFixture(t)
	writerPath := filepath.Join(t.TempDir(), "swf.db")
	writer, err := sql.Open("sqlite", writerPath)
	require.NoError(t, err)
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	var mode string
	require.NoError(t, writer.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode))
	require.Equal(t, "wal", mode)
	_, err = writer.Exec("PRAGMA wal_autocheckpoint=0")
	require.NoError(t, err)
	admin := localruntime.Context(ctx)
	require.NoError(t, sqliteruntime.InitializeProjectStorage(admin, writer))
	inventory, err := sqliteruntime.InspectProjectInstallation(admin, writer, projectcatalog.InstallationDigest([]byte("disposable store")))
	require.NoError(t, err)
	require.NoError(t, sqliteruntime.InstallProjectCatalog(admin, writer, inventory, []projectcatalog.Seed{{ID: "0", Name: "0"}}))
	var busy, logFrames, checkpointed int
	require.NoError(t, writer.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed))
	require.Zero(t, busy)
	store, err := projectcatalog.NewSQLStore(writer, projectcatalog.SQLite)
	require.NoError(t, err)
	created, err := (&projectcatalog.Service{Store: store}).CreateProject(admin, jobdb.CreateProjectRequest{Name: "not-checkpointed", RequestID: "wal-create"})
	require.NoError(t, err)
	copyProjectFixture(t, writerPath, filepath.Join(root, "swf.db"))
	copyProjectFixture(t, writerPath+"-wal", filepath.Join(root, "swf.db-wal"))
	controlPath := filepath.Join(t.TempDir(), "swf.db")
	copyProjectFixture(t, writerPath, controlPath)
	control, db, err := openProjectCatalog(controlPath)
	require.NoError(t, err)
	defer db.Close()
	query := jobdb.ListProjectsRequest{Name: created.Project.Name}
	listed, err := control.ListProjects(admin, query)
	require.NoError(t, err)
	require.Empty(t, listed.Projects)
	before := projectFixtureFiles(t, root)
	require.Len(t, before, 3)
	require.NotEmpty(t, before["swf.db-wal"])
	listed, err = ListProjects(ctx, jobdbtarget.Target{Embedded: true}, query)
	require.NoError(t, err)
	require.Len(t, listed.Projects, 1)
	require.Equal(t, created.Project.ID, listed.Projects[0].ID)
	require.Equal(t, before, projectFixtureFiles(t, root))
	require.Empty(t, projectFixtureFiles(t, scratch))
	lock, err := acquireEmbedLock(root)
	require.NoError(t, err)
	defer lock.close()
	locked := projectFixtureFiles(t, root)
	_, err = ListProjects(ctx, jobdbtarget.Target{Embedded: true}, query)
	require.ErrorIs(t, err, jobdb.ErrProjectUnavailable)
	require.ErrorContains(t, err, "already in use")
	require.Equal(t, locked, projectFixtureFiles(t, root))
}

func setupProjectFixture(t *testing.T) (string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root, err := resolveEmbedRoot()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "lock"), []byte("retained owner record\n"), 0o600))
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	return root, scratch
}

func projectFixtureFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	fileSet := map[string][]byte{}
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(root, entry.Name()))
		require.NoError(t, err)
		fileSet[entry.Name()] = contents
	}
	return fileSet
}

func copyProjectFixture(t *testing.T, source, destination string) {
	t.Helper()
	contents, err := os.ReadFile(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(destination, contents, 0o600))
}

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
