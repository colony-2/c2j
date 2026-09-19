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
		Short: "List jobs from JobDB",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			if useEmbed {
				runOpts.JobDBURI = defaults.EmbedURL
			}
			return listjobs.Run(context.Background(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")
	flags.BoolVar(&opts.Self, "self", false, "List jobs for the current cell")
	flags.StringVar(&opts.Cell, "cell", "", "List jobs for a specific cell (short name or repo/path)")
	flags.StringSliceVar(&opts.Statuses, "status", nil, "Filter by job status (repeatable)")
	flags.StringArrayVar(&opts.JobTypes, "job-type", nil, "Filter by exact job type (repeatable; values are not comma-separated)")
	flags.StringSliceVar(&opts.JobIDs, "job-id", nil, "Filter by job ID in the selected tenant (repeatable)")
	flags.StringArrayVar(&opts.WaitingFor, "waiting-for", nil, `Filter by waiting task route as JSON {"jobType":"recipe","taskType":"input:collect_user_input"} (repeatable)`)
	flags.StringVar(&opts.CreatedAfter, "created-after", "", "Filter jobs created at or after this RFC3339 timestamp")
	flags.StringVar(&opts.CreatedBefore, "created-before", "", "Filter jobs created at or before this RFC3339 timestamp")
	flags.IntVar(&opts.PageSize, "page-size", 0, "Page size for the JobDB query (0 uses the server default)")
	flags.StringVar(&opts.PageToken, "page-token", "", "Pagination token returned from a prior list call")
	flags.BoolVar(&opts.All, "all", false, "Fetch all pages instead of a single page")
	flags.BoolVar(&useEmbed, "embed", false, "Use embedded JobDB (equivalent to --jobdb "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit job data as JSON")

	cmd.AddCommand(newListChildrenCmd())

	return cmd
}
