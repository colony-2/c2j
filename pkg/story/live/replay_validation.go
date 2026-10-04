package live

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// ErrIncompleteReplay means replay omitted persisted task or job-attempt events.
// A partial story returned with this error must not be presented as complete.
var ErrIncompleteReplay = errors.New("job story replay is incomplete")

type jobRunReader interface {
	GetJobRun(context.Context, jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error)
}

type replayTaskAttempt struct {
	ordinal int64
	attempt int
}

// Track all events, including control tasks that are not rendered as recipe nodes.
// Read the persisted history before replay so a concurrently progressing job
// cannot make the validation demand events added after replay finished.
type replayEvents struct {
	jobworkflow.ReplayObserver
	mu         sync.Mutex
	jobStarts  map[int]bool
	jobEnds    map[int]jobworkflow.JobEndEvent
	taskStarts map[replayTaskAttempt]bool
	taskEnds   map[replayTaskAttempt]bool
}

func newReplayEvents(observer jobworkflow.ReplayObserver) *replayEvents {
	return &replayEvents{ReplayObserver: observer, jobStarts: map[int]bool{}, jobEnds: map[int]jobworkflow.JobEndEvent{}, taskStarts: map[replayTaskAttempt]bool{}, taskEnds: map[replayTaskAttempt]bool{}}
}
func (e *replayEvents) OnJobStart(v jobworkflow.JobStartEvent) {
	e.mu.Lock()
	e.jobStarts[v.AttemptNumber] = true
	e.mu.Unlock()
	e.ReplayObserver.OnJobStart(v)
}
func (e *replayEvents) OnJobEnd(v jobworkflow.JobEndEvent) {
	e.mu.Lock()
	e.jobEnds[v.AttemptNumber] = v
	e.mu.Unlock()
	e.ReplayObserver.OnJobEnd(v)
}
func (e *replayEvents) OnTaskStart(v jobworkflow.TaskStartEvent) {
	e.mu.Lock()
	e.taskStarts[replayTaskAttempt{v.Ordinal, v.AttemptNumber}] = true
	e.mu.Unlock()
	e.ReplayObserver.OnTaskStart(v)
}
func (e *replayEvents) OnTaskEnd(v jobworkflow.TaskEndEvent) {
	e.mu.Lock()
	e.taskEnds[replayTaskAttempt{v.Ordinal, v.AttemptNumber}] = true
	e.mu.Unlock()
	e.ReplayObserver.OnTaskEnd(v)
}

func (e *replayEvents) validate(run jobdb.GetJobRunResponse) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, job := range run.Attempts {
		for _, task := range job.Tasks {
			for _, attempt := range task.Attempts {
				if attempt.State != jobdb.TaskAttemptStateSucceeded && attempt.State != jobdb.TaskAttemptStateFailed {
					continue
				}
				key := replayTaskAttempt{attempt.Ordinal, attempt.Attempt}
				if !e.taskStarts[key] || !e.taskEnds[key] {
					return fmt.Errorf("%w: missing events for task %q ordinal %d attempt %d", ErrIncompleteReplay, task.TaskType, attempt.Ordinal, attempt.Attempt)
				}
			}
		}
		if job.Outcome.Status == jobdb.TaskOutcomeStatusSucceeded || job.Outcome.Status == jobdb.TaskOutcomeStatusFailed {
			if _, ended := e.jobEnds[job.Attempt]; !e.jobStarts[job.Attempt] || !ended {
				return fmt.Errorf("%w: missing events for job attempt %d", ErrIncompleteReplay, job.Attempt)
			}
		}
	}
	return nil
}

func (e *replayEvents) endedWith(err error) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	latest := 0
	for attempt := range e.jobEnds {
		if attempt > latest {
			latest = attempt
		}
	}
	return latest > 0 && errors.Is(e.jobEnds[latest].Err, err)
}

// JobDB may report a chapter mismatch after orchestration exits early. Retain
// the compiler's original error (for example an unknown op in stored YAML).
type replayRecipeWorker struct {
	jobworkflow.JobWorker
	mu  sync.Mutex
	err error
}

func (w *replayRecipeWorker) Run(ctx jobworkflow.JobContext, input jobdb.JobData) (jobdb.JobData, error) {
	out, err := w.JobWorker.Run(ctx, input)
	w.mu.Lock()
	w.err = err
	w.mu.Unlock()
	return out, err
}
func (w *replayRecipeWorker) withCause(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil || errors.Is(err, w.err) {
		return err
	}
	return errors.Join(err, fmt.Errorf("recipe replay: %w", w.err))
}
