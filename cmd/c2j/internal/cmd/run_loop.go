package cmd

import (
	"context"
	"os"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/workjob"
	"github.com/spf13/cobra"
)

func newRunLoopCmd() *cobra.Command {
	opts := workjob.Options{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "loop",
		Short: "Continuously run available jobs for one tenant",
		RunE: func(cmd *cobra.Command, args []string) error {
			return workjob.Run(context.Background(), opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.TenantID, "tenant-id", "", "Tenant/project ID to poll (defaults to project self.tenant_id or derived self.repo hash when available)")
	flags.StringVar(&opts.SWFURL, "swf-url", "", "External SWF runtime URL (http(s)://...; embed:/// is not supported; defaults to "+defaults.SWFURL+")")
	flags.IntVar(&opts.Concurrency, "concurrency", 1, "Maximum number of jobs to run concurrently")
	flags.DurationVar(&opts.AwaitThreshold, "await-threshold", 30*time.Second, "Await threshold before SWF reschedules instead of sleeping inline")

	return cmd
}
