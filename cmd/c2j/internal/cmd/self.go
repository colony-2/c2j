package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/configinspect"
	"github.com/spf13/cobra"
)

func newSelfCmd() *cobra.Command {
	opts := configinspect.SelfOptions{
		Stdout: os.Stdout,
	}

	cmd := &cobra.Command{
		Use:   "self",
		Short: "Show resolved information about the current cell",
		RunE: func(cmd *cobra.Command, args []string) error {
			return configinspect.RunSelf(context.Background(), opts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit the resolved self info as JSON")

	return cmd
}
