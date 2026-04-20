package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/initconfig"
	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	opts := initconfig.Options{
		Stdout: os.Stdout,
	}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a commented .c2j/config.yaml template",
		RunE: func(cmd *cobra.Command, args []string) error {
			return initconfig.Run(context.Background(), opts)
		},
	}

	flags := cmd.Flags()
	flags.BoolVar(&opts.Force, "force", false, "Overwrite an existing .c2j/config.yaml")
	flags.BoolVar(&opts.StdoutOnly, "stdout", false, "Print the generated config to stdout instead of writing it")

	return cmd
}
