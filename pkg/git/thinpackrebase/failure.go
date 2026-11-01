package thinpackrebase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const ConflictCode = "REBASE_CONFLICT"
const FailedCode = "REBASE_FAILED"

type Conflict struct {
	Path   string `json:"path"`
	Status string `json:"status"`
}

// FailureDetails is versioned independently of human-readable Git messages.
type FailureDetails struct {
	Version           int                      `json:"version"`
	Phase             string                   `json:"phase"`
	BaseHash          *string                  `json:"base_hash"`
	CandidateHash     *string                  `json:"candidate_hash"`
	TargetBaseHash    *string                  `json:"target_base_hash"`
	Conflicts         []Conflict               `json:"conflicts"`
	WorkspaceRestored bool                     `json:"workspace_restored"`
	Diagnostics       map[string]artifacts.Ref `json:"diagnostics"`
	CleanupError      string                   `json:"cleanup_error,omitempty"`
	DiagnosticErrors  []string                 `json:"diagnostic_errors,omitempty"`
}

// Failure preserves the native cause for cancellation/system classification.
// Artifact bytes are never serialized into error attributes.
type Failure struct {
	Code     string
	Details  FailureDetails
	cause    error
	evidence map[string][]byte
}

func (e *Failure) Error() string                     { return e.cause.Error() }
func (e *Failure) Unwrap() error                     { return e.cause }
func (e *Failure) FailureAttributes() map[string]any { return map[string]any{"rebase": e.Details} }
func (e *Failure) As(target any) bool {
	switch target.(type) {
	case **jobdb.AppError, *jobdb.AppError:
	default:
		return false
	}
	if nonApplicationFailure(e.cause) {
		return false
	}
	code := e.Code
	var existing *jobdb.AppError
	attrs := map[string]any{}
	if errors.As(e.cause, &existing) {
		for k, v := range existing.Payload.Attrs {
			attrs[k] = v
		}
		if specific, ok := attrs["code"].(string); ok && specific != "" {
			code = specific
		}
	}
	attrs["code"], attrs["rebase"] = code, e.Details
	app := jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: e.Error(), Attrs: attrs}}
	switch dst := target.(type) {
	case **jobdb.AppError:
		*dst = &app
	case *jobdb.AppError:
		*dst = app
	}
	return true
}

func nonApplicationFailure(err error) bool {
	var timeout *jobdb.TimeoutError
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, jobdb.ErrJobCancelled) || errors.As(err, &timeout) || jobdb.IsSystemError(err)
}

type replayError struct {
	cause  error
	output []byte
	stderr []byte
}

func (e *replayError) Error() string {
	return "git rebase failed: " + strings.TrimSpace(string(e.output))
}
func (e *replayError) Unwrap() error { return e.cause }

func optionalHash(hash string) *string {
	if hash = strings.TrimSpace(hash); hash == "" {
		return nil
	}
	return &hash
}

func captureFailure(ctx context.Context, cause error, snapshot workspaceSnapshot, target, phase string, clean, mutated bool) error {
	if context.Cause(ctx) != nil {
		cause = errors.Join(cause, context.Cause(ctx))
	}
	f := &Failure{Code: FailedCode, cause: cause, evidence: map[string][]byte{}, Details: FailureDetails{
		Version: 1, Phase: phase, BaseHash: optionalHash(snapshot.BaseHash), CandidateHash: optionalHash(snapshot.PersistHash), TargetBaseHash: optionalHash(target),
		Conflicts: []Conflict{}, Diagnostics: map[string]artifacts.Ref{}, WorkspaceRestored: !mutated,
	}}
	f.evidence["error.txt"] = []byte(cause.Error())
	var replay *replayError
	if errors.As(cause, &replay) {
		// Evidence collection cannot spend the restoration budget.
		diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		f.evidence["git-output.txt"] = replay.output
		f.evidence["git-stderr.txt"] = replay.stderr
		status, err := common.ExecuteGitCommand(diagnosticCtx, snapshot.RepoPath, "status", "--porcelain=v1", "-z", "--untracked-files=no")
		if err == nil {
			f.Details.Conflicts, err = conflictStatus(status)
		}
		if err != nil {
			f.Details.DiagnosticErrors = append(f.Details.DiagnosticErrors, "conflict status: "+err.Error())
		}
		if len(f.Details.Conflicts) > 0 && !nonApplicationFailure(cause) {
			f.Code = ConflictCode
		}
		for name, args := range map[string][]string{
			"conflict-index.z":      {"ls-files", "--unmerged", "-z"},
			"conflict.diff":         {"diff", "--no-ext-diff", "--no-textconv", "--binary"},
			"replayed-commit.patch": {"show", "--no-ext-diff", "--no-textconv", "--format=fuller", "--binary", "REBASE_HEAD"},
		} {
			data, err := common.ExecuteGitCommand(diagnosticCtx, snapshot.RepoPath, args...)
			if err != nil {
				f.Details.DiagnosticErrors = append(f.Details.DiagnosticErrors, name+": "+err.Error())
				continue
			}
			f.evidence[name] = data
		}
	}
	if mutated && clean {
		// Cleanup must be attempted even after cancellation or failed diagnostics.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var err error
		if replay != nil {
			err = abortRebase(cleanup, snapshot.RepoPath)
		} else {
			_, err = common.ExecuteGitCommand(cleanup, snapshot.RepoPath, "reset", "--hard", snapshot.PersistHash)
		}
		if err == nil {
			var head string
			head, err = common.GetCommitHash(cleanup, snapshot.RepoPath, "HEAD")
			if err == nil && head != snapshot.PersistHash {
				err = fmt.Errorf("restored HEAD differs from original candidate")
			}
			if err == nil {
				var status []byte
				status, err = common.ExecuteGitCommand(cleanup, snapshot.RepoPath, "status", "--porcelain=v1", "-z")
				if err == nil && len(status) != 0 {
					err = fmt.Errorf("workspace remains dirty after cleanup")
				}
			}
		}
		f.Details.WorkspaceRestored = err == nil
		if err != nil {
			f.Details.CleanupError = err.Error()
		}
	}
	return f
}

// Parse NUL-terminated porcelain, including the second pathname of rename/copy
// entries. Never split paths on whitespace or interpret localized stderr.
func conflictStatus(data []byte) ([]Conflict, error) {
	conflicts := []Conflict{}
	if len(data) != 0 && data[len(data)-1] != 0 {
		return nil, fmt.Errorf("unterminated Git status record")
	}
	entries := strings.Split(string(data), "\x00")
	for i := 0; i < len(entries)-1; i++ {
		entry := entries[i]
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, fmt.Errorf("invalid Git status record")
		}
		status := entry[:2]
		switch status {
		case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
			conflicts = append(conflicts, Conflict{Path: entry[3:], Status: status})
		}
		if strings.ContainsAny(status, "RC") {
			i++
		}
	}
	return conflicts, nil
}

// Attach evidence to this task's outcome, using the same durable key convention
// as object artifacts. No worker-local filenames cross the failure boundary.
func attachFailureEvidence(ctx context.Context, inv ops.OpDependencies, err error) {
	var f *Failure
	if inv == nil || !errors.As(err, &f) {
		return
	}
	for name, data := range f.evidence {
		data = []byte(secrets.RedactorFromContext(ctx).Redact(string(data)))
		name = "_c2j/rebase/" + name
		artifact := jobdb.NewArtifactFromBytes(name, data)
		if err := inv.AddOutputArtifact(artifact); err != nil {
			f.Details.DiagnosticErrors = append(f.Details.DiagnosticErrors, err.Error())
			continue
		}
		jt := inv.JobTool()
		ordinal, ok := jt.(interface{ TaskOrdinal() int64 })
		if !ok || jt.GetJobKey().JobId == "" {
			f.Details.DiagnosticErrors = append(f.Details.DiagnosticErrors, "durable artifact identity unavailable: "+name)
			continue
		}
		f.Details.Diagnostics[name] = artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: jt.GetJobKey().JobId, TaskOrdinal: ordinal.TaskOrdinal(), Name: name, SizeBytes: int64(len(data))})
	}
}
