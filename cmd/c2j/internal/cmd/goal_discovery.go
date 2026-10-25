package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func withGoalRuntime(cmd *cobra.Command, uri string, query func(jobdb.WorkflowRuntime, string) (any, error)) error {
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
	result, err := query(h.Runtime, target.TenantID)
	if err != nil {
		return err
	}
	return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
}

func newGoalListCmd(uri *string) *cobra.Command {
	var req jobdb.ListGoalsRequest
	var candidate jobdb.GoalCandidateFilter
	var states []string
	var repo, cell, createdAfter, createdBefore, updatedAfter, updatedBefore string
	cmd := &cobra.Command{Use: "list", Short: "List authoritative goals and candidate counts (one JSON page)", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		query := req
		query.States = nil
		for _, state := range states {
			query.States = append(query.States, jobdb.GoalState(strings.ToUpper(state)))
		}
		filter := candidate
		for _, selector := range []struct{ key, value string }{{"repo", repo}, {"cell_name", cell}} {
			if selector.value != "" {
				filter.MetadataEquals = append(filter.MetadataEquals, jobdb.MetadataPredicate{Path: []string{selector.key}, Values: []any{selector.value}})
			}
		}
		if len(filter.JobIDs)+len(filter.JobTypes)+len(filter.MetadataEquals) > 0 {
			query.Candidate = &filter
		}
		for _, bound := range []struct {
			raw string
			out **time.Time
		}{{createdAfter, &query.CreatedAfter}, {createdBefore, &query.CreatedBefore}, {updatedAfter, &query.UpdatedAfter}, {updatedBefore, &query.UpdatedBefore}} {
			if bound.raw != "" {
				parsed, err := time.Parse(time.RFC3339Nano, bound.raw)
				if err != nil {
					return fmt.Errorf("invalid time bound %q: %w", bound.raw, err)
				}
				*bound.out = &parsed
			}
		}
		return withGoalRuntime(cmd, *uri, func(rt jobdb.WorkflowRuntime, tenant string) (any, error) {
			query.TenantId = tenant
			return recipejob.ListGoals(cmd.Context(), rt, query)
		})
	}
	f := cmd.Flags()
	f.StringSliceVar(&req.GoalIDs, "goal", nil, "Exact goal IDs")
	f.StringSliceVar(&states, "state", nil, "Goal states: OPEN, SUCCEEDED, FAILED (default all)")
	f.StringSliceVar(&candidate.JobIDs, "candidate-job", nil, "Match candidate job IDs")
	f.StringSliceVar(&candidate.JobTypes, "job-type", nil, "Match candidate job types")
	f.StringVar(&repo, "repo", "", "Exact candidate repository metadata; counts still cover the whole goal")
	f.StringVar(&cell, "cell", "", "Exact candidate cell-name metadata")
	f.StringVar(&createdAfter, "created-after", "", "Inclusive RFC3339 creation lower bound")
	f.StringVar(&createdBefore, "created-before", "", "Inclusive RFC3339 creation upper bound")
	f.StringVar(&updatedAfter, "updated-after", "", "Inclusive RFC3339 goal-record update lower bound")
	f.StringVar(&updatedBefore, "updated-before", "", "Inclusive RFC3339 goal-record update upper bound")
	f.IntVar(&req.PageSize, "page-size", 100, "Page size (maximum 200)")
	f.StringVar(&req.PageToken, "page-token", "", "Opaque cursor; keep tenant and filters unchanged")
	return cmd
}

func newGoalWaitsCmd(uri *string) *cobra.Command {
	var req jobdb.ListDependencyWaitsRequest
	var states []string
	var goal, job string
	cmd := &cobra.Command{Use: "waits", Short: "Inspect consumer wait history or unresolved pending dependencies (JSON)", Args: cobra.NoArgs}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		query := req
		query.States = nil
		for _, state := range states {
			query.States = append(query.States, jobdb.DependencyState(strings.ToUpper(state)))
		}
		if goal != "" || job != "" {
			query.Target = &jobdb.DependencyTarget{GoalID: goal, JobID: job}
		}
		return withGoalRuntime(cmd, *uri, func(rt jobdb.WorkflowRuntime, tenant string) (any, error) {
			query.TenantId = tenant
			return recipejob.ListDependencyWaits(cmd.Context(), rt, query)
		})
	}
	f := cmd.Flags()
	f.StringVar(&req.ConsumerJobID, "consumer-job", "", "Consumer job; includes retained historical waits unless filtered")
	f.StringVar(&goal, "target-goal", "", "Goal target; only unresolved edges in pending waits")
	f.StringVar(&job, "target-job", "", "Exact job target; only unresolved edges in pending waits")
	cmd.MarkFlagsMutuallyExclusive("target-goal", "target-job")
	f.StringSliceVar(&states, "state", nil, "Wait states; target queries accept PENDING only")
	f.IntVar(&req.PageSize, "page-size", 100, "Page size (maximum 200)")
	f.StringVar(&req.PageToken, "page-token", "", "Opaque cursor; keep tenant and filters unchanged")
	return cmd
}

func newGoalWaitCmd(uri *string) *cobra.Command {
	return &cobra.Command{Use: "wait JOB WAIT", Short: "Inspect one recorded dependency wait (JSON)", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return withGoalRuntime(cmd, *uri, func(rt jobdb.WorkflowRuntime, tenant string) (any, error) {
			return recipejob.GetDependencyWait(cmd.Context(), rt, jobdb.DependencyWaitKey{JobKey: jobdb.JobKey{TenantId: tenant, JobId: args[0]}, WaitID: args[1]})
		})
	}}
}
