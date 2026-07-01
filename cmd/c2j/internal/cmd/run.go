package cmd

import (
	"context"
	"os"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	cmd := newRunOneSpecificCmd("run")
	cmd.Short = "Run jobs through JobDB"
	cmd.AddCommand(newRunOneSpecificCmd("one"))
	cmd.AddCommand(newRunAnyCmd())
	cmd.AddCommand(newRunLoopCmd())
	return cmd
}

func newRunOneSpecificCmd(use string) *cobra.Command {
	var useEmbed bool
	opts := runjob.Options{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   use,
		Short: "Run or continue one existing job through JobDB",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			if useEmbed {
				runOpts.JobDBURI = defaults.EmbedURL
			}
			return runjob.Run(context.Background(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.JobID, "job-id", "", "Job ID to execute")
	flags.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")
	flags.DurationVar(&opts.WaitTimeout, "wait-timeout", 15*time.Minute, "How long to wait on external blocking work before exiting")
	flags.DurationVar(&opts.PollInterval, "poll-interval", 5*time.Second, "Polling interval while waiting on external blocking work")
	flags.DurationVar(&opts.LeaseDuration, "lease-duration", 60*time.Second, "Lease duration requested from JobDB")
	flags.DurationVar(&opts.AwaitThreshold, "await-threshold", 30*time.Second, "Await threshold before JobDB reschedules instead of sleeping inline")
	flags.StringVar(&opts.WorkerID, "worker-id", "", "Optional worker ID for JobDB leases")
	flags.StringVar(&opts.OnNotReady, "on-not-ready", "wait", "How to handle non-ready jobs: wait|fail|fail-on-lease|fail-on-pending-jobs|fail-on-future|fail-on-missing-capability")
	flags.BoolVar(&useEmbed, "embed", false, "Use embedded JobDB (equivalent to --jobdb "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.CI, "ci", false, "Emit machine-readable input-required events instead of prompting")
	flags.StringVar(&opts.InputMode, "input-mode", "", "Input mode override: prompt|ops|fail")

	return cmd
}
