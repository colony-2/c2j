package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
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
	flags.StringVar(&opts.TenantID, "tenant-id", "", "Tenant/project ID to inspect (defaults to project self.tenant_id or derived self.repo hash when available)")
	flags.StringVar(&opts.SWFURL, "swf-url", "", "SWF runtime URL (http(s)://... or embed:///; defaults to "+defaults.SWFURL+")")

	return cmd
}
