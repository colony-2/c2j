package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
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
	invocation string
	attempt    int
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
	e.taskStarts[replayTaskAttempt{v.InvocationID, v.AttemptNumber}] = true
	e.mu.Unlock()
	e.ReplayObserver.OnTaskStart(v)
}
func (e *replayEvents) OnTaskEnd(v jobworkflow.TaskEndEvent) {
	e.mu.Lock()
	e.taskEnds[replayTaskAttempt{v.InvocationID, v.AttemptNumber}] = true
	e.mu.Unlock()
	e.ReplayObserver.OnTaskEnd(v)
}

func (e *replayEvents) validate(run jobdb.GetJobRunResponse) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	tasks := map[string]string{}
	jobScope := ""
	for _, entry := range run.Execution {
		record := entry.Record
		if record.Kind == "scope" && jobScope == "" {
			jobScope = record.ID
		}
		if record.Kind == "task" {
			var descriptor struct {
				Call jobworkflow.TaskCall `json:"call"`
			}
			if err := json.Unmarshal(record.Descriptor, &descriptor); err != nil {
				return err
			}
			tasks[record.ID] = descriptor.Call.TaskType
		}
		if record.Kind != "result" {
			continue
		}
		split := strings.LastIndex(record.ID, "/attempt:")
		if split < 0 {
			continue
		}
		base := record.ID[:split]
		attempt, err := strconv.Atoi(record.ID[split+len("/attempt:"):])
		if err != nil {
			return err
		}
		if taskType, ok := tasks[base]; ok {
			key := replayTaskAttempt{record.ID, attempt}
			if !e.taskStarts[key] || !e.taskEnds[key] {
				return fmt.Errorf("%w: missing events for task %q invocation %s", ErrIncompleteReplay, taskType, record.ID)
			}
		}
		if base == jobScope {
			if _, ended := e.jobEnds[attempt]; !e.jobStarts[attempt] || !ended {
				return fmt.Errorf("%w: missing events for job attempt %d", ErrIncompleteReplay, attempt)
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
	return latest > 0 && (errors.Is(e.jobEnds[latest].Err, err) || reflect.DeepEqual(e.jobEnds[latest].Err, err))
}

// JobDB may report a chapter mismatch after orchestration exits early. Retain
// the compiler's original error (for example an unknown op in stored YAML).
type replayRecipeWorker struct {
	jobworkflow.ExecutionWorker
	mu  sync.Mutex
	err error
}

func newReplayRecipeWorker(worker jobworkflow.ExecutionWorker) *replayRecipeWorker {
	w := &replayRecipeWorker{}
	w.ExecutionWorker = jobworkflow.ExecutionWorker{JobType: worker.JobType, Execute: func(ctx jobworkflow.ExecutionContext, input jobdb.JobData) (jobdb.JobData, error) {
		out, err := worker.Execute(ctx, input)
		w.mu.Lock()
		w.err = err
		w.mu.Unlock()
		return out, err
	}}
	return w
}
func (w *replayRecipeWorker) withCause(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil || errors.Is(err, w.err) {
		return err
	}
	cause := fmt.Errorf("recipe replay: %w", w.err)
	return errors.Join(cause, err)
}
