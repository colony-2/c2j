package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/cellregistry"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newCellLifecycleCmd(unarchive bool) *cobra.Command {
	action, description, permission := "archive", "Retire a registered cell without deleting its history", "cells.retire"
	if unarchive {
		action, description, permission = "unarchive", "Restore an archived cell in place", "cells.restore"
	}
	var uri string
	var jsonOutput, restoreSupported bool
	cmd := &cobra.Command{
		Use:   action + " NAME",
		Short: description,
		Long:  description + ". Run outside a task. Requires cells.list and cells.get, plus " + permission + " and project-write admission.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := jobdb.ValidateCellName(name); err != nil {
				return fmt.Errorf("invalid cell name %q: %w", name, err)
			}
			_, hasCurrent, _ := jobcontext.CurrentFromEnv(os.Getenv)
			_, hasBroker, _ := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
			if hasCurrent || hasBroker {
				return fmt.Errorf("cell %s is not available inside a running task", action)
			}
			ctx := cmd.Context()
			target, err := defaults.ResolveJobDBTarget(ctx, "", uri)
			if err != nil {
				return err
			}
			if target.RuntimeURL == "" {
				return fmt.Errorf("JobDB runtime URL is required")
			}
			confirmed := target.Embedded || restoreSupported
			if unarchive && !confirmed {
				return fmt.Errorf("%w; use --restore-supported only after confirming the remote restore route and backend", cellregistry.ErrRestoreSupportUnconfirmed)
			}
			handle, err := swfruntime.Open(ctx, target.RuntimeURL)
			if err != nil {
				return err
			}
			defer handle.Cleanup()
			page, err := handle.Runtime.ListCells(ctx, jobdb.ListCellsRequest{TenantID: target.TenantID, Name: name, State: "ALL", PageSize: 1})
			if err != nil {
				return fmt.Errorf("look up cell %q: %w", name, err)
			}
			if len(page.Cells) == 0 {
				return fmt.Errorf("cell %q is not registered or is not accessible: %w", name, jobdb.ErrCellNotFound)
			}
			key := jobdb.CellKey{TenantID: target.TenantID, CellID: page.Cells[0].ID}
			cell, err := handle.Runtime.GetCell(ctx, key)
			if err != nil {
				return fmt.Errorf("read cell %q: %w", name, err)
			}
			ifMatch := strconv.Quote(cell.Revision)
			if unarchive {
				cell, err = cellregistry.UnarchiveCell(ctx, handle.CellRestore, key, ifMatch, cellregistry.UnarchiveOptions{RestoreSupportConfirmed: confirmed})
			} else {
				cell, err = cellregistry.ArchiveCell(ctx, handle.Runtime, key, ifMatch)
			}
			if err != nil {
				if status, _, _ := jobdb.CellErrorStatus(err); status == http.StatusPreconditionFailed {
					return fmt.Errorf("%s cell %q: revision is stale; review the current cell state before retrying: %w", action, name, err)
				}
				return fmt.Errorf("%s cell %q: %w", action, name, err)
			}
			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cell)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", cell.ID, cell.Name, cell.State)
			return err
		},
	}
	cmd.Flags().StringVar(&uri, "jobdb", "", "JobDB URI")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit the updated cell as JSON")
	if unarchive {
		cmd.Flags().BoolVar(&restoreSupported, "restore-supported", false, "Confirm the remote restore route and backend are supported (does not grant authorization)")
	}
	return cmd
}
