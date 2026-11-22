package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/spf13/cobra"
)

func inputFailure(cmd *cobra.Command, e error) error {
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var printed *printedDiagnosticError
	if errors.As(e, &printed) {
		return e
	}
	_ = json.NewEncoder(cmd.ErrOrStderr()).Encode(jobinputs.Diagnostic("invalid_request", "input history command failed", e))
	return &printedDiagnosticError{cause: e}
}
func newJobInputsCmd() *cobra.Command {
	root := &cobra.Command{Use: "inputs", Short: "Read retained input occurrences through the authenticated task helper", Long: `Read frozen forms, accepted responses and exact retained member files.
Requires a live task helper and protocol version 1 in both CLI and worker.
Tokens are transient observations, not read authority. See GUIDE-Task-Input-History.md.`}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, e error) error { return inputFailure(cmd, e) })
	for _, operation := range []string{"list", "show", "artifact"} {
		op := operation
		q := jobinputs.Request{Version: 1, Operation: op}
		var output string
		cmd := &cobra.Command{Use: op + " JOB", Short: map[string]string{"list": "Read one native page (JOB may be self)", "show": "Read an occurrence using its entry and snapshot tokens", "artifact": "Download an exact member to a new file atomically"}[op]}
		cmd.Args = func(cmd *cobra.Command, args []string) error {
			if e := cobra.ExactArgs(1)(cmd, args); e != nil {
				return inputFailure(cmd, e)
			}
			return nil
		}
		if op == "list" {
			cmd.Flags().IntVar(&q.Limit, "limit", 50, "Page size, 1..500")
			cmd.Flags().StringVar(&q.Cursor, "cursor", "", "Native next_page_token from the previous page")
		} else {
			cmd.Flags().StringVar(&q.EntryID, "entry", "", "Native entry_id from list")
			cmd.Flags().StringVar(&q.Snapshot, "snapshot", "", "Native snapshot_token from list")
		}
		if op == "artifact" {
			cmd.Flags().StringVar(&q.ArtifactID, "member", "", "Native manifest artifact_id (never a raw key)")
			cmd.Flags().StringVar(&output, "output", "", "New destination file (stdout and existing paths are rejected)")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
			defer func() {
				if err != nil {
					err = inputFailure(cmd, err)
				}
			}()
			q.JobID = args[0]
			if e := q.Validate(); e != nil {
				return e
			}
			if op == "list" && q.Limit < 1 {
				return jobinputs.Diagnostic("invalid_request", "limit must be 1..500", nil)
			}
			for _, flag := range []string{"jobdb", "embed"} {
				if cmd.Flags().Changed(flag) {
					return jobinputs.Diagnostic("invalid_request", "history requires the authenticated helper; --"+flag+" cannot override it", nil)
				}
			}
			if op == "artifact" && (output == "" || output == "-") {
				return jobinputs.Diagnostic("invalid_request", "artifact requires --output FILE; stdout is unsupported", nil)
			}
			broker, present, e := jobcontext.ChildJobBrokerFromEnv(os.Getenv)
			if e != nil || !present {
				return jobinputs.Diagnostic("unsupported", "history requires a task helper; upgrade worker and CLI together", e)
			}
			client := childbroker.JobClient{Broker: broker}
			if op == "artifact" {
				// Reject occupied/invalid destinations before contacting the helper.
				if e := newInputDestination(output); e != nil {
					return e
				}
				stream, e := client.InputArtifact(cmd.Context(), q)
				if e != nil {
					return e
				}
				return writeInputFile(cmd.Context(), output, stream)
			}
			result, e := client.Inputs(cmd.Context(), q)
			if e != nil {
				return e
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		root.AddCommand(cmd)
	}
	return root
}
func newInputDestination(path string) error {
	if _, e := os.Lstat(path); e == nil {
		return jobinputs.Diagnostic("write", "destination already exists", os.ErrExist)
	} else if !os.IsNotExist(e) {
		return jobinputs.Diagnostic("write", "cannot inspect destination", e)
	}
	info, e := os.Stat(filepath.Dir(path))
	if e != nil {
		return jobinputs.Diagnostic("write", "destination parent unavailable", e)
	}
	if !info.IsDir() {
		return jobinputs.Diagnostic("write", "destination parent is not a directory", nil)
	}
	return nil
}

// writeInputFile publishes a verified stream with atomic no-replace semantics.
type inputStage interface {
	io.Writer
	Close() error
	Name() string
}
type inputFileOps struct {
	create func(string) (inputStage, error)
	link   func(string, string) error
	remove func(string) error
}

func writeInputFile(ctx context.Context, path string, source io.ReadCloser) error {
	return writeInputFileWith(ctx, path, source, inputFileOps{create: func(dir string) (inputStage, error) { return os.CreateTemp(dir, ".c2j-input-*") }, link: os.Link, remove: os.Remove})
}
func writeInputFileWith(ctx context.Context, path string, source io.ReadCloser, ops inputFileOps) (err error) {
	sourceClosed := false
	defer func() {
		if !sourceClosed {
			err = errors.Join(err, source.Close())
		}
	}()
	if e := newInputDestination(path); e != nil {
		return e
	}
	f, e := ops.create(filepath.Dir(path))
	if e != nil {
		return jobinputs.Diagnostic("write", "cannot stage input artifact", e)
	}
	staged := f.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			err = errors.Join(err, f.Close())
		}
		cleanup := ops.remove(staged)
		if !committed && cleanup != nil && !os.IsNotExist(cleanup) {
			err = errors.Join(err, jobinputs.Diagnostic("write", "cannot remove staged artifact", cleanup))
		}
	}()
	writer := &inputWriter{Writer: f}
	_, e = io.Copy(writer, source)
	e = errors.Join(e, source.Close())
	sourceClosed = true
	if e != nil {
		if writer.err != nil {
			return jobinputs.Diagnostic("write", "cannot write staged artifact", e)
		}
		return jobinputs.Diagnostic("read", "artifact download failed", e)
	}
	e = f.Close()
	closed = true
	if e != nil {
		return jobinputs.Diagnostic("write", "cannot close staged artifact", e)
	}
	if e = context.Cause(ctx); e != nil {
		return jobinputs.Diagnostic("canceled", "artifact download canceled before commit", e)
	}
	if e = ops.link(staged, path); e != nil {
		return jobinputs.Diagnostic("write", "cannot commit artifact without replacing destination", e)
	}
	committed = true
	return nil
}

type inputWriter struct {
	io.Writer
	err error
}

func (w *inputWriter) Write(p []byte) (int, error) {
	n, e := w.Writer.Write(p)
	if e == nil && n != len(p) {
		e = io.ErrShortWrite
	}
	if e != nil {
		w.err = e
	}
	return n, e
}
