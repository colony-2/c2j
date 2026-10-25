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
	root := &cobra.Command{Use: "goals", Short: "Inspect goals and manage their candidates (JSON output)"}
	root.PersistentFlags().StringVar(&uri, "jobdb", "", "JobDB URI (defaults from project or C2J_JOBDB)")
	for _, op := range []string{"get", "candidates", "attach", "fail"} {
		op := op
		n := 1
		if op == "attach" {
			n = 2
		}
		use := op + " GOAL"
		if op == "attach" {
			use += " JOB"
		}
		command := &cobra.Command{Use: use, Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
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
			case "attach":
				value, err = recipejob.AttachGoalCandidate(cmd.Context(), h.Runtime, jobdb.AddGoalCandidateRequest{GoalKey: key, JobID: args[1]})
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
