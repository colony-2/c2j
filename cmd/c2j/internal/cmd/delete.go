package cmd

import (
	"fmt"
	"strings"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/deletejobs"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newDeleteCmd() *cobra.Command {
	var opts deletejobs.Options
	var embed bool
	cmd := &cobra.Command{
		Use: "delete", Short: "Delete jobs selected by cell, recipe, ID, or creation age",
		Args: cobra.NoArgs,
		Long: `Hide matching jobs from normal job and schedule-run listings.
Selectors intersect; repeated cells and job IDs match any of their values.
Unfinished jobs are cancelled and leases revoked. Completed outcomes, history,
and artifacts remain accessible by direct lookup. Deletion does not reclaim disk
space, cascade to children/clones/schedules, undo external effects, or forcibly
stop worker processes. Status filtering is not supported by JobDB's deletion API.
Specify at least one selector, or --all for every visible job in the tenant.
Use --dry-run to preview counts; a preview does not reserve the matching jobs.`,
		Example: `  c2j delete --self --older-than 720h --dry-run
  c2j delete --cell backend --cell frontend --recipe build
  c2j delete --job-id JOB1 --job-id JOB2 --json
  c2j delete --all --embed --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, flag := range []string{"recipe", "created-before", "older-than"} {
				value, _ := cmd.Flags().GetString(flag)
				if cmd.Flags().Changed(flag) && strings.TrimSpace(value) == "" {
					return fmt.Errorf("%w: --%s must not be empty", jobdb.ErrInvalidJobDeletion, flag)
				}
			}
			runOpts := opts
			if embed {
				runOpts.JobDBURI = defaults.EmbedURL
			}
			runOpts.Stdout = cmd.OutOrStdout()
			return deletejobs.Run(cmd.Context(), runOpts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.JobDBURI, "jobdb", "", "JobDB URI (http(s)://host/tenant or embed:///)")
	f.BoolVar(&embed, "embed", false, "Use embedded JobDB")
	f.BoolVar(&opts.Self, "self", false, "Select jobs owned by the current cell")
	f.StringArrayVar(&opts.Cells, "cell", nil, "Select jobs owned by a cell (short name or repo/path; repeatable)")
	f.StringArrayVar(&opts.JobIDs, "job-id", nil, "Select an exact job ID (repeatable)")
	f.StringVar(&opts.Recipe, "recipe", "", "Select an exact recipe name")
	f.StringVar(&opts.CreatedBefore, "created-before", "", "Exclusive creation-time cutoff (RFC3339)")
	f.StringVar(&opts.OlderThan, "older-than", "", "Creation age as a positive Go duration, e.g. 720h or 30m")
	f.BoolVar(&opts.All, "all", false, "Select every visible job in the tenant; cannot combine with selectors")
	f.BoolVar(&opts.DryRun, "dry-run", false, "Count matching jobs without deleting")
	f.BoolVar(&opts.JSONOutput, "json", false, "Emit resolved selection and counts as JSON")
	return cmd
}
