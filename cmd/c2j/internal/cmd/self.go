package cmd

import (
	"github.com/colony-2/c2j/cmd/c2j/internal/configinspect"
	"github.com/spf13/cobra"
)

func newSelfCmd() *cobra.Command {
	opts := configinspect.SelfOptions{}

	cmd := &cobra.Command{
		Use:   "self",
		Short: "Show current cell, task, lease, and available actions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			runOpts.Stdout = cmd.OutOrStdout()
			return configinspect.RunSelf(cmd.Context(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit cell and execution discovery as JSON")

	return cmd
}
