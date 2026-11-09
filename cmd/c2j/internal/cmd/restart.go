package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/spf13/cobra"
)

const restartHelp = `Discover boundaries with 'c2j job restart-points JOB --json'.
Inspect one with 'c2j job restart-context JOB --scope ID --output before.json'.
Submit with 'c2j restart JOB --scope ID --request-id KEY --patch-file patch.json'.

Choose exactly one --scope ID or explicit --chapter ORDINAL (0 means submission).
Ambiguous chapters require a unique scope ID; duplicate identities are unavailable.
Patch files contain optional typed job edits and sequence/states local step outputs:
  {"scopes":[{"container":"sequence","id":"write","outputs":{"path":"design.md"}}]}
Inputs, config, workspace and cell_resolution cannot be patched. Submission is read-only.
Snapshots preserve deferred references and redact secrets; they are not patch documents.
A patch replaces the previous patch at that boundary; omitting it removes that patch.
Earlier retained patches remain. Null deletes map keys; arrays replace whole arrays.
Reuse a request ID only to retry the same payload; use a new ID for changed requests.
A restart creates an independent continuation. Tasks after the boundary and external
side effects may repeat. Corrections after submission require another restart.`

type restartOpener func(context.Context, string) (jobrestart.API, func(), string, error)

func openRestart(ctx context.Context, uri string) (jobrestart.API, func(), string, error) {
	current, present, err := jobcontext.CurrentFromEnv(os.Getenv)
	if err != nil {
		return nil, nil, "", err
	}
	broker, helper, err := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
	if err != nil {
		return nil, nil, "", err
	}
	if helper {
		return childbroker.JobClient{Broker: broker}, func() {}, current.JobID, nil
	}
	if present {
		return nil, nil, "", fmt.Errorf("task restart helper unavailable; upgrade the worker and CLI together")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, "", err
	}
	target, err := defaults.ResolveJobDBTarget(ctx, cwd, uri)
	if err != nil {
		return nil, nil, "", err
	}
	h, err := swfruntime.Open(ctx, target.RuntimeURL)
	if err != nil {
		return nil, nil, "", err
	}
	c2jops.Register()
	// JobDB requires a registered workset even when replay supplies its own
	// read-only worker. Keep this private inspection engine off the live driver.
	replayEngine, err := jobworkflow.NewEngineBuilder().WithRuntime(h.Runtime).WithWorkerTenantId(target.TenantID).PlusWorkers(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
	if err != nil {
		_ = h.Cleanup()
		return nil, nil, "", err
	}
	service := &restartservice.Service{Engine: replayEngine, Tenant: target.TenantID}
	service.Submit = func(ctx context.Context, key jobdb.JobKey, scope string, patch *task.ContextPatch, opts starter.RestartOptions) (jobref.Ref, error) {
		return starter.RestartRecipeJob(ctx, h.Engine, key, scope, patch, opts)
	}
	return service, func() { _ = h.Cleanup() }, "", nil
}
func newRestartCmd() *cobra.Command { return newRestartCommand(openRestart) }
func newRestartCommand(open restartOpener) *cobra.Command {
	var uri, scope, mode, id, patchFile string
	var chapter int64
	command := &cobra.Command{Use: "restart JOB", Short: "Restart from a recorded scope, continuing its goal by default", Long: restartHelp, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		selector, err := restartSelector(cmd, scope, chapter)
		if err != nil {
			return err
		}
		var patch *task.ContextPatch
		if cmd.Flags().Changed("patch-file") {
			raw, err := os.ReadFile(patchFile)
			if err != nil {
				return err
			}
			patch, err = jobrestart.ParsePatch(raw)
			if err != nil {
				return err
			}
		}
		api, close, current, err := open(cmd.Context(), uri)
		if err != nil {
			return err
		}
		defer close()
		job, err := restartJob(args[0], current)
		if err != nil {
			return err
		}
		result, err := api.Restart(cmd.Context(), jobrestart.Request{Operation: "submit", JobID: job, Selector: selector, Patch: patch, Goal: mode, RequestID: id})
		if err != nil {
			return err
		}
		if result == nil || result.Ref == nil {
			return fmt.Errorf("restart helper returned no submission identity")
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Ref)
	}}
	command.Flags().StringVar(&uri, "jobdb", "", "JobDB URI (defaults from project or C2J_JOBDB)")
	addRestartSelector(command, &scope, &chapter)
	command.Flags().StringVar(&mode, "goal", "continue", "Goal mode: continue or new")
	command.Flags().StringVar(&id, "request-id", "", "Stable request ID; reuse only for the same restart payload")
	command.Flags().StringVar(&patchFile, "patch-file", "", "JSON context patch file")
	_ = command.MarkFlagRequired("request-id")
	return command
}
func addRestartSelector(cmd *cobra.Command, scope *string, chapter *int64) {
	cmd.Flags().StringVar(scope, "scope", "", "Recorded restartable scope ID")
	cmd.Flags().Int64Var(chapter, "chapter", 0, "Unambiguous recorded chapter ordinal (explicit 0 selects submission)")
}
func restartSelector(cmd *cobra.Command, scope string, chapter int64) (jobrestart.Selector, error) {
	selector := jobrestart.Selector{Scope: scope}
	if cmd.Flags().Changed("chapter") {
		selector.Chapter = &chapter
	}
	if cmd.Flags().Changed("scope") == cmd.Flags().Changed("chapter") {
		return selector, fmt.Errorf("exactly one --scope or --chapter is required")
	}
	return selector, selector.Validate()
}
func restartJob(job, current string) (string, error) {
	if job != "self" {
		return job, nil
	}
	if current == "" {
		return "", fmt.Errorf("self requires a current task")
	}
	return current, nil
}
func newRestartInspection(operation string, uri *string, embed, jsonOutput *bool, open restartOpener) *cobra.Command {
	var scope, output string
	var chapter int64
	name := "restart-points"
	if operation == "context" {
		name = "restart-context"
	}
	command := &cobra.Command{Use: name + " JOB", Short: map[string]string{"points": "List recorded eligible restart boundaries without replay", "context": "Inspect the complete context before a restart boundary"}[operation], Long: restartHelp, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := jobrestart.Request{Operation: operation}
		if operation == "context" {
			var err error
			request.Selector, err = restartSelector(cmd, scope, chapter)
			if err != nil {
				return err
			}
		}
		target := *uri
		if *embed {
			target = defaults.EmbedURL
		}
		api, close, current, err := open(cmd.Context(), target)
		if err != nil {
			return err
		}
		defer close()
		request.JobID, err = restartJob(args[0], current)
		if err != nil {
			return err
		}
		response, err := api.Restart(cmd.Context(), request)
		if err != nil {
			return err
		}
		if response == nil {
			return fmt.Errorf("restart helper returned no result")
		}
		if operation == "context" {
			if response.Context == nil {
				return fmt.Errorf("restart context unavailable")
			}
			return writeInspectionFile(restartOutput(output), cmd.OutOrStdout(), func(w io.Writer) error { return json.NewEncoder(w).Encode(response.Context) })
		}
		if response.Points == nil {
			return fmt.Errorf("restart points unavailable")
		}
		if *jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Points)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "SCOPE ID\tCHAPTER\tSTARTED AT\tTITLE\tPATH\tUNAVAILABLE REASON")
		for _, point := range response.Points.RestartScopes {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%v\t%s\n", point.ScopeID, point.ChapterOrdinal, point.StartedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), point.Title, point.Path, point.UnavailableReason)
		}
		return w.Flush()
	}}
	if operation == "context" {
		addRestartSelector(command, &scope, &chapter)
		command.Flags().StringVar(&output, "output", "", "Write complete snapshot JSON to a new file (or - for stdout)")
	}
	return command
}

func restartOutput(path string) string {
	if path == "" {
		return "-"
	}
	return path
}
