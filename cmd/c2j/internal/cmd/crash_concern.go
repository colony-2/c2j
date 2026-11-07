package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newClearCrashConcernCmd(uri *string, embed, jsonOutput *bool) *cobra.Command {
	var reason, worker string
	command := &cobra.Command{
		Use:     "clear-crash-concern JOB",
		Aliases: []string{"reset-crash-concern"},
		Short:   "Clear crash concern so the existing job can be scheduled again",
		Long: `Reset the job's consecutive expired-lease count using operator credentials.
Job history and scheduling constraints are preserved. Workers can resume the
existing job when eligible; this command does not execute it. Jobs with a live
lease, and completed, cancelled, or hidden jobs, cannot be cleared.
The task inspection helper does not grant this operator action.`,
		Example: "  c2j job clear-crash-concern JOB --reason 'worker issue fixed'\n  c2j job clear-crash-concern JOB --embed --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURI := *uri
			if *embed {
				targetURI = defaults.EmbedURL
			}
			target, err := defaults.ResolveJobDBTarget(cmd.Context(), "", targetURI)
			if err != nil {
				return err
			}
			if target.TenantID == "" || target.RuntimeURL == "" {
				return fmt.Errorf("--jobdb is required (or %s, or project jobdb)", defaults.JobDBEnv)
			}
			req := jobdb.ClearCrashConcernRequest{
				JobKey:   jobdb.JobKey{TenantId: target.TenantID, JobId: args[0]},
				WorkerID: worker, Reason: reason,
			}
			if err := req.Validate(); err != nil {
				return err
			}
			handle, err := swfruntime.Open(cmd.Context(), target.RuntimeURL)
			if err != nil {
				return fmt.Errorf("open JobDB runtime: %w", err)
			}
			defer handle.Cleanup()
			if err := recipejob.ClearCrashConcern(cmd.Context(), handle.Runtime, req); err != nil {
				return fmt.Errorf("clear crash concern for job %s/%s: %w", target.TenantID, args[0], err)
			}
			if *jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					TenantID string `json:"tenant_id"`
					JobID    string `json:"job_id"`
					Cleared  bool   `json:"cleared"`
				}{target.TenantID, args[0], true})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Cleared crash concern for job %s/%s.\n", target.TenantID, args[0])
			return err
		},
	}
	command.Flags().StringVar(&reason, "reason", "", "Operator's reason for clearing crash concern")
	command.Flags().StringVar(&worker, "worker-id", "c2j-cli", "Operator identifier for backend tracing")
	return command
}
