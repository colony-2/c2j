package localruntime

import (
	"context"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type adminCellRestore struct {
	restore jobdb.CellRestoreRuntime
}

// NewAdminCellRestore adapts the optional restore port for trusted embedded
// administration only. It does not grant restore on the ordinary runtime.
func NewAdminCellRestore(restore jobdb.CellRestoreRuntime) jobdb.CellRestoreRuntime {
	if restore == nil {
		return nil
	}
	return adminCellRestore{restore: restore}
}

func (adapter adminCellRestore) RestoreCell(ctx context.Context, req jobdb.RestoreCellRequest) (jobdb.Cell, error) {
	ctx = Context(ctx)
	authority, _ := jobdb.CellAuthorityFromContext(ctx)
	authority.Permissions["cells.restore"] = true
	return adapter.restore.RestoreCell(jobdb.WithCellAuthority(ctx, authority), req)
}
