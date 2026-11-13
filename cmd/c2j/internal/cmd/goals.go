package cmd

import (
	"encoding/json"
	"os"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func newGoalsCmd() *cobra.Command {
	var uri, message, token string
	var size int
	root := &cobra.Command{
		Use: "goals", Short: "Inspect goals and declare failure (JSON output)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.PersistentFlags().StringVar(&uri, "jobdb", "", "JobDB URI (defaults from project or C2J_JOBDB)")
	for _, op := range []string{"get", "candidates", "fail"} {
		op := op
		use := op + " GOAL"
		command := &cobra.Command{Use: use, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
			key := jobdb.GoalKey{TenantId: target.TenantID, GoalID: args[0]}
			var value any
			switch op {
			case "get":
				value, err = recipejob.GetGoal(cmd.Context(), h.Runtime, key)
			case "candidates":
				value, err = recipejob.ListGoalCandidates(cmd.Context(), h.Runtime, recipejob.GoalCandidatesRequest{TenantID: key.TenantId, GoalID: key.GoalID, PageSize: size, PageToken: token})
			case "fail":
				value, err = recipejob.FailGoal(cmd.Context(), h.Runtime, jobdb.FailGoalRequest{GoalKey: key, Message: message})
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
		}}
		if op == "fail" {
			command.Flags().StringVar(&message, "message", "", "Permanent failure reason (does not cancel candidates)")
			_ = command.MarkFlagRequired("message")
		}
		if op == "candidates" {
			command.Flags().IntVar(&size, "page-size", 100, "Maximum candidates per page")
			command.Flags().StringVar(&token, "page-token", "", "Cursor from previous page")
		}
		root.AddCommand(command)
	}
	root.AddCommand(newGoalListCmd(&uri), newGoalWaitsCmd(&uri), newGoalWaitCmd(&uri))
	return root
}
