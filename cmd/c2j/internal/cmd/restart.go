package cmd

import (
	"encoding/json"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newRestartCmd() *cobra.Command {
	var uri, scope, mode, id string
	command := &cobra.Command{Use: "restart JOB", Short: "Restart from a recorded scope, continuing its goal by default", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		target, err := defaults.ResolveJobDBTarget(cmd.Context(), cwd, uri)
		if err != nil {
			return err
		}
		h, err := swfruntime.Open(cmd.Context(), target.RuntimeURL)
		if err != nil {
			return err
		}
		defer h.Cleanup()
		ref, err := starter.RestartRecipeJob(cmd.Context(), h.Engine, jobdb.JobKey{TenantId: target.TenantID, JobId: args[0]}, scope, nil, starter.RestartOptions{Goal: mode, RequestID: id})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(ref)
	}}
	command.Flags().StringVar(&uri, "jobdb", "", "JobDB URI (defaults from project or C2J_JOBDB)")
	command.Flags().StringVar(&scope, "scope", "", "Recorded restartable scope ID")
	command.Flags().StringVar(&mode, "goal", "continue", "Goal mode: continue or new")
	command.Flags().StringVar(&id, "request-id", "", "Stable request ID; reuse it when retrying this restart")
	_ = command.MarkFlagRequired("scope")
	_ = command.MarkFlagRequired("request-id")
	return command
}
