package commandop

import (
	"context"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const (
	StdoutArtifactName = "_c2j/command/stdout.txt"
	StderrArtifactName = "_c2j/command/stderr.txt"
)

// FailureDetails contains only metadata and durable refs, never stream bytes.
type FailureDetails struct {
	Version          int                      `json:"version"`
	ExitCode         int                      `json:"exit_code"`
	TimedOut         bool                     `json:"timed_out"`
	Diagnostics      map[string]artifacts.Ref `json:"diagnostics"`
	DiagnosticErrors []string                 `json:"diagnostic_errors,omitempty"`
}

// commandFailure adds evidence without changing the message or classification
// of its cause. The task worker persists these attributes even on native errors.
type commandFailure struct {
	cause   error
	message string
	details FailureDetails
}

func (e *commandFailure) Error() string { return e.message }
func (e *commandFailure) Unwrap() error { return e.cause }
func (e *commandFailure) FailureAttributes() map[string]any {
	return map[string]any{"command": e.details}
}

func captureFailure(ctx context.Context, deps ops.OpDependencies, cause error, message string, exitCode int, timedOut bool, stdout, stderr []byte) error {
	f := &commandFailure{cause: cause, message: message, details: FailureDetails{
		Version: 1, ExitCode: exitCode, TimedOut: timedOut, Diagnostics: map[string]artifacts.Ref{},
	}}
	redactor := secrets.RedactorFromContext(ctx)
	for _, stream := range []struct {
		name string
		data []byte
	}{{StdoutArtifactName, stdout}, {StderrArtifactName, stderr}} {
		data := []byte(redactor.Redact(string(stream.data)))
		if err := deps.AddOutputArtifact(jobdb.NewArtifactFromBytes(stream.name, data)); err != nil {
			f.details.DiagnosticErrors = append(f.details.DiagnosticErrors, redactor.Redact(err.Error()))
			continue
		}
		jt := deps.JobTool()
		ordinal, ok := jt.(interface{ TaskOrdinal() int64 })
		if !ok || jt.GetJobKey().JobId == "" {
			f.details.DiagnosticErrors = append(f.details.DiagnosticErrors, "durable artifact identity unavailable: "+stream.name)
			continue
		}
		// JobDB gives each task attempt its own result ordinal, including retries.
		f.details.Diagnostics[stream.name] = artifacts.NewStoredRef(jobdb.ArtifactKey{
			JobId: jt.GetJobKey().JobId, TaskOrdinal: ordinal.TaskOrdinal(), Name: stream.name, SizeBytes: int64(len(data)),
		})
	}
	return f
}
