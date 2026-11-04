package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/colony-2/c2j/cmd/c2j/internal/configinspect"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newCellsCmd() *cobra.Command {
	var uri, state string
	var jsonOutput, routing, all bool
	cmd := &cobra.Command{Use: "cells", Short: "List cells from the JobDB registry", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if routing {
			return configinspect.RunCells(cmd.Context(), configinspect.CellsOptions{Stdout: cmd.OutOrStdout(), JSONOutput: jsonOutput})
		}
		target, err := defaults.ResolveJobDBTarget(cmd.Context(), "", uri)
		if err != nil {
			return err
		}
		handle, err := swfruntime.Open(cmd.Context(), target.RuntimeURL)
		if err != nil {
			return err
		}
		defer handle.Cleanup()
		cells := []jobdb.Cell{}
		req := jobdb.ListCellsRequest{TenantID: target.TenantID, State: state}
		for {
			page, err := handle.Runtime.ListCells(cmd.Context(), req)
			if err != nil {
				return err
			}
			cells = append(cells, page.Cells...)
			if page.NextPageToken == "" {
				break
			}
			req.PageToken = page.NextPageToken
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(cells)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATE\tREPO")
		for _, cell := range cells {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", cell.ID, cell.Name, cell.State, cell.Repo)
		}
		return w.Flush()
	}
	f := cmd.Flags()
	f.StringVar(&uri, "jobdb", "", "JobDB URI")
	f.StringVar(&state, "state", "ACTIVE", "Cell state: ACTIVE, RETIRED, or ALL")
	f.BoolVar(&jsonOutput, "json", false, "Emit cells as JSON")
	f.BoolVar(&routing, "dependencies", false, "List configured dependent routes")
	f.BoolVar(&all, "all", false, "List the visible JobDB cell registry (default)")
	cmd.MarkFlagsMutuallyExclusive("dependencies", "all")
	return cmd
}
