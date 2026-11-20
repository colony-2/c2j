package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/spf13/cobra"
)

type printedDiagnosticError struct{ cause error }

func (e *printedDiagnosticError) Error() string { return e.cause.Error() }
func (e *printedDiagnosticError) Unwrap() error { return e.cause }

func newJobChangesCommands(uri *string, embed, jsonOutput *bool, open inspectionOpener) []*cobra.Command {
	var commands []*cobra.Command
	for _, op := range []string{"changes", "diff"} {
		var task, scope, workspace, boundary, output string
		var attempt int
		cmd := &cobra.Command{Use: op + " JOB", Short: "Read the saved cumulative workspace diff at a recorded point", Args: cobra.ExactArgs(1), SilenceUsage: true}
		cmd.Long = cmd.Short + ". Raw saved bytes by default; --json reads metadata only.\nUse job log and job story for exact task/scope occurrence IDs and attempts.\nNo Git, commit history, reconstruction, or comparison-base override is used."
		if op == "changes" {
			cmd.Short = "Deprecated metadata alias for job diff --json"
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
			defer func() {
				if err != nil && *jsonOutput {
					var printed *printedDiagnosticError
					if !errors.As(err, &printed) {
						_ = json.NewEncoder(cmd.ErrOrStderr()).Encode(jobinspect.WorkspaceDiffDiagnostic("output", "saved workspace diff failed", err))
						err = &printedDiagnosticError{cause: err}
					}
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
				}
			}()
			for _, name := range []string{"all", "commit", "from", "through", "base", "selection", "cursor", "limit"} {
				if cmd.Flags().Changed(name) {
					return jobinspect.WorkspaceDiffDiagnostic("invalid_request", "--"+name+" was removed; use --task or --scope with --attempt, --workspace and --boundary for saved cumulative diffs; --json reads metadata", nil)
				}
			}
			if *jsonOutput && cmd.Flags().Changed("output") {
				return jobinspect.WorkspaceDiffDiagnostic("invalid_request", "--json conflicts with --output; omit --output for metadata or omit --json for raw bytes", nil)
			}
			q := jobinspect.WorkspaceDiffRequest{JobID: args[0]}
			if cmd.Flags().Changed("task") {
				q.Task = &task
			}
			if cmd.Flags().Changed("scope") {
				q.Scope = &scope
			}
			if cmd.Flags().Changed("attempt") {
				q.Attempt = &attempt
			}
			if cmd.Flags().Changed("workspace") {
				q.Workspace = &workspace
			}
			if cmd.Flags().Changed("boundary") {
				q.Boundary = &boundary
			}
			if err = q.Validate(); err != nil {
				return err
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
			if q.JobID == "self" {
				if current == "" {
					return fmt.Errorf("self requires a current task")
				}
				q.JobID = current
			}
			if op == "changes" {
				fmt.Fprintln(cmd.ErrOrStderr(), "job changes is deprecated; use job diff --json")
			}
			var meta *jobinspect.WorkspaceDiff
			var body io.ReadCloser
			if *jsonOutput || op == "changes" {
				r, e := api.Inspect(cmd.Context(), jobinspect.Request{Operation: "workspace_diff", JobID: q.JobID, WorkspaceDiff: &q.WorkspaceDiffOptions})
				if e != nil {
					return e
				}
				meta = r.WorkspaceDiff
				if err = jobinspect.ValidateWorkspaceDiff(q, meta); err != nil {
					return err
				}
				if err = writeInspectionFile(output, cmd.OutOrStdout(), func(w io.Writer) error { return json.NewEncoder(w).Encode(meta) }); err != nil {
					return err
				}
			} else {
				reader, ok := api.(jobinspect.WorkspaceDiffAPI)
				if !ok {
					return jobinspect.WorkspaceDiffDiagnostic("unsupported", "saved diff unavailable; upgrade worker and CLI together", nil)
				}
				meta, body, err = reader.OpenWorkspaceDiff(cmd.Context(), q)
				if err != nil {
					return err
				}
				if body != nil {
					defer body.Close()
				}
				if err = jobinspect.ValidateWorkspaceDiff(q, meta); err != nil {
					return err
				}
				if meta.Status != "unavailable" {
					if body == nil {
						return jobinspect.WorkspaceDiffDiagnostic("protocol", "saved patch stream is missing", nil)
					}
					if err = writeInspectionFile(output, cmd.OutOrStdout(), func(w io.Writer) error {
						n, e := io.Copy(w, body)
						e = errors.Join(e, body.Close())
						if e != nil {
							return e
						}
						if meta.Bytes != nil && n != *meta.Bytes {
							return jobinspect.WorkspaceDiffDiagnostic("byte_count", "incomplete diff output", nil)
						}
						return nil
					}); err != nil {
						return err
					}
				}
			}
			if meta.Status == "unavailable" {
				return jobinspect.WorkspaceDiffDiagnostic("unavailable", "saved cumulative comparison unavailable: "+fmt.Sprint(meta.Reasons), nil)
			}
			return nil
		}
		f := cmd.Flags()
		f.StringVar(&task, "task", "", "Exact native task occurrence ID")
		f.StringVar(&scope, "scope", "", "Exact native scope occurrence ID")
		f.IntVar(&attempt, "attempt", 0, "Positive attempt (omitted: latest recorded)")
		f.StringVar(&workspace, "workspace", "", "Exact workspace ID or main")
		f.StringVar(&boundary, "boundary", "", "before or after (omitted: after)")
		f.StringVar(&output, "output", "-", "Write to a new file; - writes stdout")
		f.Bool("all", false, "Removed: use saved point selectors")
		for _, name := range []string{"commit", "from", "through", "base", "selection", "cursor", "limit"} {
			f.String(name, "", "Removed: use saved point selectors")
			_ = f.MarkHidden(name)
		}
		_ = f.MarkHidden("all")
		commands = append(commands, cmd)
	}
	return commands
}
