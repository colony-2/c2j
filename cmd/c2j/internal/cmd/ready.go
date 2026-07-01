package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/workjob"
	"github.com/spf13/cobra"
)

func newReadyCmd() *cobra.Command {
	opts := workjob.ReadyOptions{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "ready",
		Short: "Count ready jobs for one tenant",
		RunE: func(cmd *cobra.Command, args []string) error {
			return workjob.Ready(context.Background(), opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")

	return cmd
}
