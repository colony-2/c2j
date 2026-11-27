// Package cellregistry provides administrative operations on registered cells.
package cellregistry

import (
	"context"
	"errors"
	"fmt"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

var (
	// ErrRestoreSupportUnconfirmed means no restore request was made.
	ErrRestoreSupportUnconfirmed = errors.New("JobDB cell restore support is unconfirmed")
	// ErrRestoreOutcomeUnconfirmed means the restore call did not return a definitive result.
	ErrRestoreOutcomeUnconfirmed = errors.New("cell restore outcome is unconfirmed; verify deployment support and cell state before retrying")
)

type UnarchiveOptions struct {
	// RestoreSupportConfirmed must only be set when configuration or an operator
	// confirms both the deployment's restore route and its backend support.
	// Confirmation is not authorization.
	RestoreSupportConfirmed bool
}

// ArchiveCell retires the cell at the caller's confirmed revision. It passes
// the strong If-Match through unchanged and does not retry on conflicts.
func ArchiveCell(ctx context.Context, cells jobdb.CellRuntime, key jobdb.CellKey, ifMatch string) (jobdb.Cell, error) {
	return cells.RetireCell(ctx, jobdb.RetireCellRequest{CellKey: key, IfMatch: ifMatch})
}

// UnarchiveCell reactivates the same cell at the caller's confirmed revision.
// It passes the strong If-Match through unchanged, without probing or retrying.
func UnarchiveCell(ctx context.Context, restore jobdb.CellRestoreRuntime, key jobdb.CellKey, ifMatch string, opts UnarchiveOptions) (jobdb.Cell, error) {
	if !opts.RestoreSupportConfirmed {
		return jobdb.Cell{}, ErrRestoreSupportUnconfirmed
	}
	if restore == nil {
		return jobdb.Cell{}, jobdb.ErrCellRestoreUnsupported
	}
	cell, err := restore.RestoreCell(ctx, jobdb.RestoreCellRequest{CellKey: key, IfMatch: ifMatch})
	if err != nil {
		if _, _, structured := jobdb.CellErrorStatus(err); !structured {
			return jobdb.Cell{}, fmt.Errorf("%w: %w", ErrRestoreOutcomeUnconfirmed, err)
		}
	}
	return cell, err
}
