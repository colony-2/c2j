package cmd

import (
	"context"
	"os"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/workjob"
	"github.com/spf13/cobra"
)

func newRunAnyCmd() *cobra.Command {
	opts := workjob.RunOneOptions{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "any",
		Short: "Poll and run one available job for one tenant",
		RunE: func(cmd *cobra.Command, args []string) error {
			return workjob.RunOne(context.Background(), opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")
	flags.DurationVar(&opts.LeaseDuration, "lease-duration", 60*time.Second, "Lease duration requested from JobDB")
	flags.DurationVar(&opts.AwaitThreshold, "await-threshold", 30*time.Second, "Await threshold before JobDB reschedules instead of sleeping inline")

	return cmd
}
