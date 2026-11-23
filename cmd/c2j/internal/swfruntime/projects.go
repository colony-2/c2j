package swfruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/colony-2/c2j/internal/localruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/projectcatalog"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
)

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
