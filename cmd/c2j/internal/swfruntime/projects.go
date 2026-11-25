package swfruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/internal/jobdbtarget"
	"github.com/colony-2/c2j/internal/localruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/projectcatalog"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
)

func CreateProject(ctx context.Context, target jobdbtarget.Target, request jobdb.CreateProjectRequest) (result jobdb.ProjectCreation, err error) {
	if !target.Embedded {
		catalog, err := remoteruntime.NewProjectRuntime(target.RuntimeURL, newHTTPClient())
		if err != nil {
			return result, err
		}
		created, err := catalog.CreateProject(ctx, request)
		return created, remoteProjectCollectionError(err)
	}
	root, err := resolveEmbedRoot()
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return result, err
	}
	lock, err := acquireEmbedLock(root)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lock.close()) }()
	if err := prepareEmbedProjects(ctx, root); err != nil {
		return result, err
	}
	catalog, db, err := openProjectCatalog(filepath.Join(root, "swf.db"))
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return catalog.CreateProject(localruntime.Context(ctx), request)
}

func ListProjects(ctx context.Context, target jobdbtarget.Target, request jobdb.ListProjectsRequest) (result jobdb.ListProjectsResponse, err error) {
	if !target.Embedded {
		catalog, err := remoteruntime.NewProjectRuntime(target.RuntimeURL, newHTTPClient())
		if err != nil {
			return result, err
		}
		listed, err := catalog.ListProjects(ctx, request)
		return listed, remoteProjectCollectionError(err)
	}
	root, err := resolveEmbedRoot()
	if err != nil {
		return result, err
	}
	snapshot, err := snapshotEmbedProjects(root)
	if err != nil {
		return result, fmt.Errorf("%w: %w", jobdb.ErrProjectUnavailable, err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(snapshot)) }()
	catalog, db, err := openProjectCatalog(filepath.Join(snapshot, "swf.db"))
	if err != nil {
		return result, fmt.Errorf("%w: %w", jobdb.ErrProjectUnavailable, err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return catalog.ListProjects(localruntime.Context(ctx), request)
}

func remoteProjectCollectionError(err error) error {
	if errors.Is(err, jobdb.ErrProjectNotFound) {
		return jobdb.ErrProjectUnavailable
	}
	return err
}

func openProjectCatalog(path string) (*projectcatalog.Service, *sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	store, err := projectcatalog.NewSQLStore(db, projectcatalog.SQLite)
	if err != nil {
		return nil, nil, errors.Join(err, db.Close())
	}
	return &projectcatalog.Service{Store: store}, db, nil
}

// Never open the live database with SQLite for list: even mode=ro can create
// sidecars. The owner lock makes the main database and WAL a consistent pair.
func snapshotEmbedProjects(root string) (snapshot string, err error) {
	lock, err := lockEmbedRoot(root, false)
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, lock.close())
		if err != nil && snapshot != "" {
			err = errors.Join(err, os.RemoveAll(snapshot))
			snapshot = ""
		}
	}()
	path := filepath.Join(root, "swf.db")
	if _, err := os.Lstat(path + "-journal"); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return "", err
		}
		return "", errors.New("embedded database has a rollback journal; offline inspection is required")
	}
	tempRoot, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", err
	}
	storeRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	storeRoot, err = filepath.Abs(storeRoot)
	if err != nil {
		return "", err
	}
	tempRoot, err = filepath.Abs(tempRoot)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(storeRoot, tempRoot)
	if err != nil {
		return "", err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", errors.New("temporary directory must be outside the embedded store")
	}
	snapshot, err = os.MkdirTemp(tempRoot, "c2j-project-catalog-")
	if err != nil {
		return "", err
	}
	if err := copyProjectFile(path, filepath.Join(snapshot, "swf.db"), false); err != nil {
		return snapshot, err
	}
	return snapshot, copyProjectFile(path+"-wal", filepath.Join(snapshot, "swf.db-wal"), true)
}

func copyProjectFile(source, dest string, optional bool) (err error) {
	info, err := os.Lstat(source)
	if optional && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("embedded database file is not regular: %s", source)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	output, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	_, err = io.Copy(output, input)
	return err
}

// prepareEmbedProjects runs under the embedded runtime's exclusive owner lock,
// before opening any blob or secret provider. Only a new, otherwise empty root
// can be initialized automatically. Existing state requires offline migration
// and an owner inventory of independent participants; opening it never imports
// tenant IDs or invents that inventory.
func prepareEmbedProjects(ctx context.Context, root string) error {
	path := filepath.Join(root, "swf.db")
	_, err := os.Lstat(path)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return err
	}
	if fresh {
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != "lock" {
				return fmt.Errorf("%w: embedded root contains prior state; explicit project installation required", jobdb.ErrProjectUnavailable)
			}
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx = localruntime.Context(ctx)
	if fresh {
		if err := sqliteruntime.InitializeProjectStorage(ctx, db); err != nil {
			return err
		}
		// All providers use the default paths in this newly created root; no
		// provider, backup, key or resource writer predates initialization.
		participants := projectcatalog.InstallationDigest([]byte("c2j embedded fresh root v1: exclusive lock; empty root; default local blob and dev-secret stores; no prior providers, backups, keys or writers"))
		inventory, err := sqliteruntime.InspectProjectInstallation(ctx, db, participants)
		if err != nil {
			return err
		}
		if err := sqliteruntime.InstallProjectCatalog(ctx, db, inventory, []projectcatalog.Seed{{ID: "0", Name: "0"}, {ID: "c2", Name: "c2"}}); err != nil {
			return err
		}
	}
	store, err := projectcatalog.NewSQLStore(db, projectcatalog.SQLite)
	if err != nil {
		return err
	}
	_, err = (&projectcatalog.Service{Store: store}).ListProjects(ctx, jobdb.ListProjectsRequest{PageSize: 1})
	if err != nil {
		return fmt.Errorf("%w: embedded store requires explicit offline project installation (including independent providers) before use: %v", jobdb.ErrProjectUnavailable, err)
	}
	return nil
}
