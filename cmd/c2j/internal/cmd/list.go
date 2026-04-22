package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/listjobs"
	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var useEmbed bool
	opts := listjobs.Options{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List jobs from the SWF runtime",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			if useEmbed {
				runOpts.SWFURL = defaults.EmbedURL
			}
			return listjobs.Run(context.Background(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.TenantID, "tenant-id", "", "Tenant/project ID to query (defaults to "+defaults.TenantID+")")
	flags.StringVar(&opts.SWFURL, "swf-url", "", "SWF runtime URL (http(s)://... or embed:///; defaults to "+defaults.SWFURL+")")
	flags.BoolVar(&opts.Self, "self", false, "List jobs for the current cell")
	flags.StringVar(&opts.Cell, "cell", "", "List jobs for a specific cell (short name or repo/path)")
	flags.StringSliceVar(&opts.Statuses, "status", nil, "Filter by job status (repeatable)")
	flags.StringSliceVar(&opts.JobTypes, "job-type", nil, "Filter by job type (repeatable)")
	flags.StringSliceVar(&opts.JobIDs, "job-id", nil, "Filter by job ID in the selected tenant (repeatable)")
	flags.StringSliceVar(&opts.WaitingFor, "waiting-for", nil, "Filter by waiting capability in JOBTYPE:TASKTYPE form (repeatable)")
	flags.StringVar(&opts.CreatedAfter, "created-after", "", "Filter jobs created at or after this RFC3339 timestamp")
	flags.StringVar(&opts.CreatedBefore, "created-before", "", "Filter jobs created at or before this RFC3339 timestamp")
	flags.IntVar(&opts.PageSize, "page-size", 0, "Page size for the SWF query (0 uses the server default)")
	flags.StringVar(&opts.PageToken, "page-token", "", "Pagination token returned from a prior list call")
	flags.BoolVar(&opts.All, "all", false, "Fetch all pages instead of a single page")
	flags.BoolVar(&useEmbed, "embed", false, "Use the embedded SWF runtime (equivalent to --swf-url "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit job data as JSON")

	return cmd
}
