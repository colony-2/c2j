package cmd

import (
	"context"
	"os"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/spf13/cobra"
)

func newExecCmd() *cobra.Command {
	var useEmbed bool
	opts := runjob.Options{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Execute or continue an existing job through the SWF runtime",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			if useEmbed {
				runOpts.SWFURL = defaults.EmbedURL
			}
			return runjob.Run(context.Background(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.JobID, "job-id", "", "Job ID to execute")
	flags.StringVar(&opts.TenantID, "tenant-id", "", "Tenant/project ID for the job (defaults to project self.tenant_id or derived self.repo hash when available)")
	flags.StringVar(&opts.SWFURL, "swf-url", "", "SWF runtime URL (http(s)://... or embed:///; defaults to "+defaults.SWFURL+")")
	flags.DurationVar(&opts.WaitTimeout, "wait-timeout", 15*time.Minute, "How long to wait on external blocking work before exiting")
	flags.DurationVar(&opts.PollInterval, "poll-interval", 5*time.Second, "Polling interval while waiting on external blocking work")
	flags.DurationVar(&opts.LeaseDuration, "lease-duration", 60*time.Second, "Lease duration requested from SWF")
	flags.DurationVar(&opts.AwaitThreshold, "await-threshold", 30*time.Second, "Await threshold before SWF reschedules instead of sleeping inline")
	flags.StringVar(&opts.WorkerID, "worker-id", "", "Optional worker ID for SWF leases")
	flags.StringVar(&opts.OnNotReady, "on-not-ready", "wait", "How to handle non-ready jobs: wait|fail|fail-on-lease|fail-on-pending-jobs|fail-on-future|fail-on-missing-capability")
	flags.BoolVar(&useEmbed, "embed", false, "Use the embedded SWF runtime (equivalent to --swf-url "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.CI, "ci", false, "Emit machine-readable input-required events instead of prompting")
	flags.StringVar(&opts.InputMode, "input-mode", "", "Input mode override: prompt|ops|fail")

	return cmd
}
