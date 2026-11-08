package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
)

func openInspection(ctx context.Context, uri string) (jobinspect.API, func(), string, error) {
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
		return nil, nil, "", fmt.Errorf("task job inspection helper unavailable; upgrade the worker and CLI together")
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
	return &jobinspect.Service{Runtime: h.Runtime, Tenant: target.TenantID}, func() { _ = h.Cleanup() }, "", nil
}

func newJobCmd() *cobra.Command {
	var uri string
	var embed, jsonOutput bool
	root := &cobra.Command{Use: "job", Short: "Inspect jobs and clear crash concern", Long: `Inspect persisted jobs and recover jobs blocked by crash concern.

Start with 'c2j job log JOB'. Copy a task ID from that log to read its output,
download an artifact, or export the cumulative workspace at that point.
Inside a task, JOB may be 'self'; reads follow JobDB permissions for the selected tenant.
Use 'c2j job clear-crash-concern JOB' with operator credentials to restore
scheduling eligibility after repeated worker crashes.

Example:
  c2j job log JOB --json
  c2j job log JOB --task TASK --field /summary
  c2j job snapshot JOB --task TASK --dir ./recovered

The recovered directory contains Git checkouts, artifacts, bundles, and snapshot.json.
Use 'git -C CHECKOUT diff refs/c2j/base HEAD' to review cumulative changes.`}
	root.PersistentFlags().StringVar(&uri, "jobdb", "", "JobDB URI (defaults from project or C2J_JOBDB)")
	root.PersistentFlags().BoolVar(&embed, "embed", false, "Use embedded JobDB")
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
	root.AddCommand(newClearCrashConcernCmd(&uri, &embed, &jsonOutput))
	for _, operation := range []string{"events", "list", "show", "log", "story", "result", "fields", "artifacts", "artifact", "snapshot"} {
		op := operation
		q := jobinspect.Request{Operation: op}
		var output, dir, field, reference string
		var statuses []string
		var eventTypes []string
		var after, before string
		short := map[string]string{"events": "Read one page of retained lease events", "fields": "List output field pointers, types, sizes, and bounded previews", "list": "List jobs (use --parent JOB for children)", "show": "Show job identity and status", "log": "List task occurrences or inspect one task's outputs", "story": "List recorded execution scopes without recipe replay", "result": "Inspect final recipe outputs and exported artifacts", "artifacts": "List artifacts exported by a job or produced by one task", "artifact": "Download one artifact exactly", "snapshot": "Inspect or export cumulative Git state and artifacts"}[op]
		use := op + " JOB"
		args := cobra.ExactArgs(1)
		if op == "list" {
			use = op
			args = cobra.NoArgs
		}
		command := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			request := q
			if op == "events" {
				for _, value := range eventTypes {
					request.Events.EventTypes = append(request.Events.EventTypes, jobdb.LeaseEventType(value))
				}
				for _, bound := range []struct {
					value string
					dest  **time.Time
				}{{after, &request.Events.OccurredAfter}, {before, &request.Events.OccurredBefore}} {
					if bound.value != "" {
						at, err := time.Parse(time.RFC3339Nano, bound.value)
						if err != nil {
							return err
						}
						*bound.dest = &at
					}
				}
			}
			if len(args) > 0 {
				request.JobID = args[0]
			}
			if cmd.Flags().Changed("field") {
				request.Field = &field
			}
			if reference != "" {
				var key jobdb.ArtifactKey
				if err := json.Unmarshal([]byte(reference), &key); err != nil {
					return fmt.Errorf("ref must be the stored key JSON returned by inspection: %w", err)
				}
				request.Reference = &key
			}
			for _, status := range statuses {
				request.Status = append(request.Status, jobdb.JobStatus(strings.ToUpper(status)))
			}
			if dir != "" && output != "" {
				return fmt.Errorf("--dir and --output are mutually exclusive")
			}
			if op == "artifact" && output == "" {
				return fmt.Errorf("artifact download requires --output FILE (or - for stdout)")
			}
			if op == "artifact" && request.Name == "" && request.Reference == nil {
				return fmt.Errorf("choose --name or --ref from the artifact inventory")
			}
			target := uri
			if embed {
				target = defaults.EmbedURL
			}
			api, close, current, err := openInspection(cmd.Context(), target)
			if err != nil {
				return err
			}
			defer close()
			if request.JobID == "self" {
				if current == "" {
					return fmt.Errorf("self requires a current task")
				}
				request.JobID = current
			}
			if op == "artifact" {
				return writeInspectionFile(output, cmd.OutOrStdout(), func(w io.Writer) error {
					r, e := api.Download(cmd.Context(), request)
					if e != nil {
						return e
					}
					defer r.Close()
					_, e = io.Copy(w, r)
					return e
				})
			}
			if op == "snapshot" && dir != "" {
				snapshot, err := jobinspect.Export(cmd.Context(), api, request, dir)
				if err != nil {
					return err
				}
				if jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(snapshot)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Snapshot saved to %s\nMetadata: %s/snapshot.json\n", dir, dir)
				for _, w := range snapshot.Workspaces {
					fmt.Fprintf(cmd.OutOrStdout(), "Workspace %s: %s/%s\n", w.Cell, dir, w.Directory)
					if w.BaseCommit != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "  git -C %q diff refs/c2j/base HEAD\n", dir+"/"+w.Directory)
					}
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Artifacts: %d (paths recorded in snapshot.json)\n", len(snapshot.Artifacts))
				return nil
			}
			response, err := api.Inspect(cmd.Context(), request)
			if err != nil {
				return err
			}
			var raw []byte
			if op == "events" {
				raw, err = json.MarshalIndent(response.LeaseEvents, "", "  ")
				if err != nil {
					return err
				}
			} else if request.Field != nil {
				raw = response.Value
			} else {
				raw, err = json.MarshalIndent(response, "", "  ")
				if err != nil {
					return err
				}
			}
			if output != "" {
				return writeInspectionFile(output, cmd.OutOrStdout(), func(w io.Writer) error { _, e := w.Write(append(raw, '\n')); return e })
			}
			if len(raw) > 256<<10 {
				return fmt.Errorf("result exceeds terminal limit; use --output FILE for exact JSON or a smaller --limit")
			}
			if jsonOutput || request.Field != nil {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return err
			}
			return renderInspection(cmd.OutOrStdout(), response)
		}}
		f := command.Flags()
		if op == "events" {
			f.IntVar(&q.Events.PageSize, "page-size", 0, "Events per page (1–200; default 100)")
			f.StringVar(&q.Events.PageToken, "page-token", "", "Opaque continuation token")
			f.StringSliceVar(&eventTypes, "event-type", nil, "Event type: lease_acquired or lease_renewed (repeatable)")
			f.StringVar(&after, "occurred-after", "", "Inclusive RFC3339 time bound")
			f.StringVar(&before, "occurred-before", "", "Exclusive RFC3339 time bound")
			f.BoolVar(&q.Events.IncludeHidden, "include-hidden", false, "Include hidden history under JobDB policy")
		}
		f.StringVar(&output, "output", "", "Write exact JSON or artifact bytes to a new file; - writes stdout")
		if op == "list" || op == "log" || op == "story" || op == "fields" || op == "artifacts" {
			f.IntVar(&q.Limit, "limit", 25, "Items per page (1–100)")
			f.StringVar(&q.Cursor, "cursor", "", "Continuation cursor from the previous page")
		}
		if op == "log" || op == "fields" || op == "artifacts" || op == "artifact" || op == "snapshot" {
			f.StringVar(&q.Task, "task", "", "Exact task occurrence ID from job log")
			f.IntVar(&q.Attempt, "attempt", 0, "Task attempt number (default: latest)")
		}
		if op == "log" || op == "result" {
			f.StringVar(&field, "field", "", "Read an exact JSON Pointer; empty string selects the entire output")
		}
		if op == "artifact" {
			f.StringVar(&q.Name, "name", "", "Artifact name from its inventory")
			f.StringVar(&reference, "ref", "", "Exact stored artifact key as JSON")
		}
		if op == "snapshot" {
			f.StringVar(&dir, "dir", "", "Create a new directory with Git checkouts, artifacts, and snapshot.json")
			f.StringVar(&q.Workspace, "workspace", "", "Select an exact workspace scope ID or cell")
			f.StringVar(&q.Boundary, "boundary", "after", "Workspace boundary: before or after the selected task")
		}
		if op == "list" {
			f.StringVar(&q.Parent, "parent", "", "List children of this job")
			f.StringSliceVar(&statuses, "status", nil, "Filter lifecycle statuses (repeatable)")
		}
		root.AddCommand(command)
	}
	return root
}

func writeInspectionFile(path string, stdout io.Writer, write func(io.Writer) error) (err error) {
	if path == "-" {
		return write(stdout)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	return write(f)
}

func renderInspection(out io.Writer, r *jobinspect.Response) error {
	if r.LeaseEvents != nil {
		for _, event := range r.LeaseEvents.Events {
			fmt.Fprintf(out, "%s  %s  %s  worker=%s lease=%s\n", event.OccurredAt.Format(time.RFC3339Nano), event.EventID, event.EventType, event.WorkerID, event.LeaseID)
		}
		if r.LeaseEvents.NextPageToken != "" {
			fmt.Fprintf(out, "Next page: --page-token %s\n", r.LeaseEvents.NextPageToken)
		}
		return nil
	}
	if r.Job != nil {
		fmt.Fprintf(out, "Job %s: %s %s — cell %s\n", r.Job.ID, r.Job.Status, r.Job.CompletionStatus, r.Job.Cell)
		if r.Job.CompletionDetail != "" {
			fmt.Fprintf(out, "Completion detail:\n%s\n", r.Job.CompletionDetail)
		}
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if r.Jobs != nil {
		fmt.Fprintln(w, "JOB\tSTATUS\tCELL\tPARENT")
		for _, j := range r.Jobs {
			fmt.Fprintf(w, "%s\t%s %s\t%s\t%s\n", j.ID, j.Status, j.CompletionStatus, j.Cell, j.Parent)
		}
	}
	if r.Tasks != nil {
		fmt.Fprintln(w, "TASK\tTYPE / PATH\tATTEMPT\tSTATUS\tOUTPUT")
		for _, t := range r.Tasks {
			fmt.Fprintf(w, "%s\t%s %s\t%d\t%s\t%s\n", t.ID, t.Type, t.Path, t.Attempt, t.Status, t.OutputAvailability)
		}
	}
	if r.Task != nil {
		t := r.Task
		fmt.Fprintf(w, "Task %s, attempt %d: %s\n", t.ID, t.Attempt, t.Status)
		if len(t.Error) > 0 {
			fmt.Fprintf(w, "Error: %s\n", t.Error)
		}
		renderFields(w, t.Fields)
		renderArtifacts(w, t.Artifacts)
		if t.InventoryTruncated {
			fmt.Fprintln(w, "Inventory truncated; use job fields or job artifacts with --task TASK.")
		}
	}
	if r.Result != nil {
		fmt.Fprintf(w, "Final output: %s\n", r.Result.Availability)
		renderFields(w, r.Result.Fields)
		renderArtifacts(w, r.Result.Artifacts)
		if r.Result.InventoryTruncated {
			fmt.Fprintln(w, "Inventory truncated; use job fields or job artifacts.")
		}
	}
	for _, sc := range r.Scopes {
		fmt.Fprintf(w, "%s\t%s\t%s\n", sc.ID, sc.Site, sc.Status)
	}
	renderArtifacts(w, r.Artifacts)
	renderFields(w, r.Fields)
	if r.Snapshot != nil {
		for _, s := range r.Snapshot.Workspaces {
			fmt.Fprintf(w, "Workspace %s (%s): %s\n  base: %s\n  head: %s\n", s.Cell, s.ScopeID, s.Availability, s.BaseCommit, s.HeadCommit)
			if s.Reason != "" {
				fmt.Fprintln(w, "  "+s.Reason)
			}
		}
		fmt.Fprintf(w, "Artifacts through this point: %d\nUse --dir NEW_DIRECTORY to export checkouts and artifacts.\n", len(r.Snapshot.Artifacts))
	}
	if r.NextCursor != "" {
		fmt.Fprintf(w, "Next page: --cursor %s\n", r.NextCursor)
	}
	for _, warning := range r.Warnings {
		fmt.Fprintln(w, "Incomplete: "+warning)
	}
	if len(r.Tasks) > 0 {
		fmt.Fprintln(w, "Use --task ID to inspect outputs; job snapshot JOB --task ID --dir DIR exports cumulative state.")
	}
	return w.Flush()
}
func renderFields(w io.Writer, fields []jobinspect.Field) {
	for _, f := range fields {
		fmt.Fprintf(w, "%s\t%s (%d bytes)\t%s\n", f.Pointer, f.Type, f.Bytes, f.Preview)
	}
}
func renderArtifacts(w io.Writer, arts []jobinspect.Artifact) {
	for _, a := range arts {
		fmt.Fprintf(w, "Artifact %s\t%d bytes\t%s\n", a.Name, a.Size, a.Availability)
	}
}
