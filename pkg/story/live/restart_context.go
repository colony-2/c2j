package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/redact"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

var ErrInvalidRestartBoundary = errors.New("invalid restart boundary")
var ErrRestartContextUnavailable = errors.New("restart context unavailable")

func RestartBoundary(history []jobdb.ExecutionHistoryEntry, ordinal int64) (jobdb.ExecutionHistoryEntry, error) {
	var found *jobdb.ExecutionHistoryEntry
	for _, entry := range history {
		if entry.Ordinal == ordinal && restartableScope(entry) {
			if found != nil {
				return jobdb.ExecutionHistoryEntry{}, fmt.Errorf("%w: chapter %d contains multiple scopes; use scope ID", ErrInvalidRestartBoundary, ordinal)
			}
			copy := entry
			found = &copy
		}
	}
	if found != nil {
		return *found, nil
	}
	return jobdb.ExecutionHistoryEntry{}, fmt.Errorf("%w: chapter %d is not a recorded patchable scope entry", ErrInvalidRestartBoundary, ordinal)
}

func RestartBoundaryByID(history []jobdb.ExecutionHistoryEntry, id string) (jobdb.ExecutionHistoryEntry, error) {
	for _, entry := range history {
		if entry.Record.ID == id && restartableScope(entry) {
			return entry, nil
		}
	}
	return jobdb.ExecutionHistoryEntry{}, fmt.Errorf("%w: scope ID is not a recorded patchable scope entry", ErrInvalidRestartBoundary)
}

// InspectRestartContext replays only as far as the selected scope's entry.
// A replay control error unwinds synchronously before the selected body runs.
func InspectRestartContext(ctx context.Context, engine replayJobRunner, key jobdb.JobKey, history []jobdb.ExecutionHistoryEntry, ordinal int64, provider template.CELOptionsProvider, resolver compiler.RecipeSourceResolver, loggers ...*slog.Logger) (snapshot *model.RestartContext, resultErr error) {
	boundary, err := RestartBoundary(history, ordinal)
	if err != nil {
		return nil, err
	}
	return inspectRestartBoundary(ctx, engine, key, history, boundary, provider, resolver, loggers...)
}

func InspectRestartContextByScopeID(ctx context.Context, engine replayJobRunner, key jobdb.JobKey, history []jobdb.ExecutionHistoryEntry, id string, provider template.CELOptionsProvider, resolver compiler.RecipeSourceResolver, loggers ...*slog.Logger) (*model.RestartContext, error) {
	boundary, err := RestartBoundaryByID(history, id)
	if err != nil {
		return nil, err
	}
	return inspectRestartBoundary(ctx, engine, key, history, boundary, provider, resolver, loggers...)
}

func inspectRestartBoundary(ctx context.Context, engine replayJobRunner, key jobdb.JobKey, history []jobdb.ExecutionHistoryEntry, boundary jobdb.ExecutionHistoryEntry, provider template.CELOptionsProvider, resolver compiler.RecipeSourceResolver, loggers ...*slog.Logger) (snapshot *model.RestartContext, resultErr error) {
	ordinal := boundary.Ordinal
	var logger *slog.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	compilerLogger := secrets.MaskLogger(logger).With("purpose", "restart_context")
	logger = compilerLogger.With("tenant_id", key.TenantId, "job_id", key.JobId)
	started := time.Now()
	defer func() {
		outcome := "succeeded"
		if resultErr != nil {
			outcome = "failed"
		}
		logger.Debug("Restart context inspection finished", "component", "story", "event", "restart_context.inspection.finished", "outcome", outcome, "chapter_ordinal", ordinal, "duration_ms", time.Since(started).Milliseconds(), "error", resultErr)
	}()
	if resolver == nil {
		resolver = replayRootSourceResolver{}
	}
	var mu sync.Mutex
	var result *model.RestartContext
	var captureErr error
	scopes := newScopeReplay(history, func(entry jobdb.ExecutionHistoryEntry, state *template.ResolutionContext) error {
		if entry.Record.ID != boundary.Record.ID {
			return nil
		}
		job, visible, err := state.RestartContextSnapshot()
		mu.Lock()
		captureErr = err
		if err == nil {
			result = &model.RestartContext{ProjectID: key.TenantId, JobID: key.JobId, ChapterOrdinal: ordinal, ScopeID: boundary.Record.ID, Job: job, Visible: visible, RedactedPaths: []string{}}
			result.Job = redactRestartValue("", "/job", job, &result.RedactedPaths).(map[string]any)
			result.Visible = redactRestartValue("", "/visible", visible, &result.RedactedPaths).(map[string]any)
			sort.Strings(result.RedactedPaths)
		}
		mu.Unlock()
		// JobDB recognizes context.Canceled as a control error, so it does not
		// replace it with a recorded outcome or evaluate a catch/retry. Return
		// it without canceling the context: parent callbacks must wait for their
		// children to finish unwinding before adopting any resolver frames.
		return context.Canceled
	})
	worker := scopes.worker(compiler.NewRecipeJobWorker(compiler.RecipeJobWorkerOptions{
		Logger:         compilerLogger,
		ReadOnlyReplay: true, CELOptionsProvider: provider, RootSourceResolver: resolver, BeforeScope: scopes.beforeScope,
	}))
	observer := scopeReplayObserver{ReplayObserver: restartContextObserver{}, scopes: scopes}
	_, replayErr := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: worker, Observer: observer})
	mu.Lock()
	defer mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if result != nil {
		return result, nil
	}
	if cause := errors.Join(captureErr, replayErr); cause != nil {
		return nil, fmt.Errorf("%w at chapter %d: %w", ErrRestartContextUnavailable, ordinal, cause)
	}
	return nil, fmt.Errorf("%w: chapter %d was not reached during replay", ErrRestartContextUnavailable, ordinal)
}

type restartContextObserver struct{}

func (restartContextObserver) OnJobStart(jobworkflow.JobStartEvent)   {}
func (restartContextObserver) OnJobEnd(jobworkflow.JobEndEvent)       {}
func (restartContextObserver) OnTaskStart(jobworkflow.TaskStartEvent) {}
func (restartContextObserver) OnTaskEnd(jobworkflow.TaskEndEvent)     {}

// Apply the shared redaction policy while recording unambiguous JSON pointers.
func redactRestartValue(path, pointer string, value any, redacted *[]string) any {
	filtered := redact.Value(path, value)
	if s, ok := filtered.(string); ok && s == redact.Redacted {
		*redacted = append(*redacted, pointer)
		return filtered
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			key := strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
			out[k] = redactRestartValue(path+"."+k, pointer+"/"+key, item, redacted)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = redactRestartValue(path, pointer+"/"+strconv.Itoa(i), item, redacted)
		}
		return out
	default:
		return filtered
	}
}
