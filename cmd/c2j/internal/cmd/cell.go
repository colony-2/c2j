package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/colony-2/c2j/cmd/c2j/internal/celledit"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newCellCmd() *cobra.Command {
	group := &cobra.Command{Use: "cell", Short: "Create and list JobDB cells or configure dependent routes"}
	var description string
	var stdoutOnly bool
	add := &cobra.Command{
		Use:   "add <repo-or-short-name>",
		Short: "Add or update a manually listed dependent cell",
		Long: "Add or update a dependent cell in the nearest project config, preserving comments.\n" +
			"Detected cells are used directly; this command does not replace automatic discovery.\n" +
			"Descriptions can be added to manually listed cells. Run c2j cell list --dependencies to list routes.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := celledit.Options{Cell: args[0], StdoutOnly: stdoutOnly, Stdout: cmd.OutOrStdout()}
			if cmd.Flags().Changed("description") {
				opts.Description = &description
			}
			return celledit.Run(cmd.Context(), opts)
		},
	}
	add.Flags().StringVar(&description, "description", "", "Set the description (empty clears it; omitted preserves it)")
	add.Flags().BoolVar(&stdoutOnly, "stdout", false, "Print proposed config YAML without writing")
	list := newCellsCmd()
	list.Use = "list"
	group.AddCommand(add, newCellCreateCmd(), list)
	return group
}

func newCellCreateCmd() *cobra.Command {
	var uri, repo, description string
	var jsonOutput bool
	cmd := &cobra.Command{Use: "create NAME", Short: "Register a cell, including the reserved root cell", Args: cobra.ExactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := jobdb.ValidateCellName(args[0]); err != nil {
			return fmt.Errorf("invalid cell name %q: %w", args[0], err)
		}
		normalized, err := jobdb.NormalizeCellRepo(repo)
		if err != nil {
			return fmt.Errorf("--repo must be a portable repository locator: %w", err)
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
		cell, err := handle.Runtime.CreateCell(cmd.Context(), jobdb.CreateCellRequest{TenantID: target.TenantID, Name: args[0], Repo: normalized, Description: description})
		if err != nil {
			return fmt.Errorf("create cell %q: %w", args[0], err)
		}
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(cell)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", cell.ID, cell.Name, cell.Repo)
		return err
	}
	f := cmd.Flags()
	f.StringVar(&uri, "jobdb", "", "JobDB URI")
	f.StringVar(&repo, "repo", "", "Repository locator (required; no Git repository is created)")
	f.StringVar(&description, "description", "", "Cell description")
	f.BoolVar(&jsonOutput, "json", false, "Emit the registered cell as JSON")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
