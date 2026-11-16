package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/spf13/cobra"
)

type changesOpener func(context.Context, string) (jobinspect.ChangesAPI, func(), string, error)

// Execute preserves the failure exit code while avoiding a second plain-text
// diagnostic from main after this command has successfully emitted JSON.
type printedDiagnosticError struct{ cause error }

func (e *printedDiagnosticError) Error() string { return e.cause.Error() }
func (e *printedDiagnosticError) Unwrap() error { return e.cause }

func openChanges(ctx context.Context, uri string) (jobinspect.ChangesAPI, func(), string, error) {
	api, close, current, err := openInspection(ctx, uri)
	if err != nil {
		return nil, nil, "", err
	}
	changes, ok := api.(jobinspect.ChangesAPI)
	if !ok {
		close()
		return nil, nil, "", fmt.Errorf("Changes unavailable; upgrade worker and CLI together")
	}
	return changes, close, current, nil
}

func newJobChangesCommands(uri *string, embed, jsonOutput *bool, open changesOpener) []*cobra.Command {
	var commands []*cobra.Command
	for _, op := range []string{"changes", "diff"} {
		q := jobinspect.DiffRequest{}
		output := "-"
		cmd := &cobra.Command{Use: op + " JOB", Short: map[string]string{"changes": "Read latest recorded main-workspace commit history", "diff": "Read an All, single or inclusive-range endpoint patch"}[op], Args: cobra.ExactArgs(1)}
		cmd.Long = cmd.Short + ". Offline by default. Uses existing bundles; writes no job records.\nAn old selection or cursor fails after the recorded workspace advances."
		cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
			defer func() {
				if err != nil && *jsonOutput {
					if e := json.NewEncoder(cmd.ErrOrStderr()).Encode(jobinspect.ChangesDiagnostic(err)); e == nil {
						err = &printedDiagnosticError{cause: err}
					}
					cmd.SilenceErrors = true
					cmd.SilenceUsage = true
				}
			}()
			target := *uri
			if *embed {
				target = defaults.EmbedURL
			}
			api, close, current, err := open(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer close()
			q.JobID = args[0]
			if q.JobID == "self" {
				if current == "" {
					return fmt.Errorf("self requires a current task")
				}
				q.JobID = current
			}
			if op == "changes" {
				h, e := api.History(cmd.Context(), q.ChangesRequest)
				if e != nil {
					return e
				}
				if e = writeInspectionFile(output, cmd.OutOrStdout(), func(w io.Writer) error { return json.NewEncoder(w).Encode(h) }); e != nil {
					return e
				}
				if !h.Complete {
					return h.Diagnostic
				}
				return nil
			}
			meta, body, err := api.OpenDiff(cmd.Context(), q)
			if err != nil {
				return err
			}
			defer body.Close()
			if err = writeInspectionFile(output, cmd.OutOrStdout(), func(w io.Writer) error {
				n, e := io.Copy(w, body)
				ce := body.Close()
				if e != nil {
					return e
				}
				if ce != nil {
					return ce
				}
				if n != meta.Bytes {
					return &jobinspect.ChangesError{Version: 1, Code: "transport", Detail: "incomplete diff output"}
				}
				return nil
			}); err != nil {
				return err
			}
			if *jsonOutput || meta.Diagnostic != nil {
				return json.NewEncoder(cmd.ErrOrStderr()).Encode(meta)
			}
			return nil
		}
		f := cmd.Flags()
		f.StringVar(&q.Base, "base", "", "Explicit full-hash comparison base (otherwise recorded active base)")
		f.StringVar(&q.Selection, "selection", "", "Expected selection returned by job changes; reject advancement")
		f.StringVar(&output, "output", "-", "New output file; - writes JSON history or raw patch to stdout")
		if op == "changes" {
			f.IntVar(&q.Limit, "limit", 100, "Commit rows per page (1–500)")
			f.StringVar(&q.Cursor, "cursor", "", "Selection-bound continuation cursor")
		} else {
			f.BoolVar(&q.All, "all", false, "Compare selected base to recorded head (default)")
			f.StringVar(&q.Commit, "commit", "", "Compare predecessor to this full-hash member")
			f.StringVar(&q.From, "from", "", "First full-hash member of an inclusive range")
			f.StringVar(&q.Through, "through", "", "Last full-hash member of an inclusive range")
		}
		commands = append(commands, cmd)
	}
	return commands
}
