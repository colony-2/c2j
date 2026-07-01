package cmd

import (
	"context"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/childjobs"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/spf13/cobra"
)

func newListChildrenCmd() *cobra.Command {
	var useEmbed bool
	opts := childjobs.Options{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}

	cmd := &cobra.Command{
		Use:   "children",
		Short: "List child recipe jobs started by the current op",
		RunE: func(cmd *cobra.Command, args []string) error {
			runOpts := opts
			if useEmbed {
				runOpts.JobDBURI = defaults.EmbedURL
			}
			return childjobs.Run(context.Background(), runOpts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")
	flags.StringVar(&opts.ParentTenantID, "parent-tenant-id", "", "Parent tenant ID (defaults from current op env)")
	flags.StringVar(&opts.ParentJobID, "parent-job-id", "", "Parent job ID (defaults from current op env)")
	flags.StringVar(&opts.ParentInvocationHash, "parent-invocation-hash", "", "Parent op invocation hash (defaults from current op env)")
	flags.BoolVar(&opts.AllParentInvocations, "all-ops", false, "List children from all ops in the parent job")
	flags.StringSliceVar(&opts.Statuses, "status", nil, "Filter by job status (repeatable)")
	flags.StringVar(&opts.CreatedAfter, "created-after", "", "Filter jobs created at or after this RFC3339 timestamp")
	flags.StringVar(&opts.CreatedBefore, "created-before", "", "Filter jobs created at or before this RFC3339 timestamp")
	flags.IntVar(&opts.PageSize, "page-size", 0, "Page size for the JobDB query (0 uses the server default)")
	flags.StringVar(&opts.PageToken, "page-token", "", "Pagination token returned from a prior list call")
	flags.BoolVar(&opts.All, "all", false, "Fetch all pages instead of a single page")
	flags.BoolVar(&useEmbed, "embed", false, "Use embedded JobDB (equivalent to --jobdb "+defaults.EmbedURL+")")
	flags.BoolVar(&opts.JSONOutput, "json", false, "Emit child job data as JSON")

	return cmd
}
