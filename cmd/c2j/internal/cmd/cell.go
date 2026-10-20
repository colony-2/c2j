package cmd

import (
	"github.com/colony-2/c2j/cmd/c2j/internal/celledit"
	"github.com/spf13/cobra"
)

func newCellCmd() *cobra.Command {
	group := &cobra.Command{Use: "cell", Short: "Manage dependent cells in project config"}
	var description string
	var stdoutOnly bool
	add := &cobra.Command{
		Use:   "add <repo-or-short-name>",
		Short: "Add or update a manually listed dependent cell",
		Long: "Add or update a dependent cell in the nearest project config, preserving comments.\n" +
			"Detected cells are used directly; this command does not replace automatic discovery.\n" +
			"Descriptions can be added to manually listed cells. Run c2j cells to list cells.",
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
	group.AddCommand(add)
	return group
}
