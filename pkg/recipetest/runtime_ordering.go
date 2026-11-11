package recipetest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// event and recordEntry are called with the fixture mutex held.
func (f *runtimeFixture) event(e OrderingEvent) {
	e.Position = len(f.report.Events) + 1
	f.report.Events = append(f.report.Events, e)
}

func (f *runtimeFixture) recordEntry(identity, job, cell, path string) {
	if f.entrySeen == nil {
		f.entrySeen = map[string]bool{}
	}
	if f.entrySeen[identity] {
		return
	}
	f.entrySeen[identity] = true
	if job != f.rootJobID {
		return
	}
	f.event(OrderingEvent{Kind: "operation_entry", JobID: job, Cell: cell, NodePath: path})
	for i, response := range f.c.Runtime.Responses {
		gate := response.AfterCall
		if gate == nil || gate.NodePath != path || (gate.Cell != "" && gate.Cell != cell) {
			continue
		}
		f.gateCounts[i]++
		if f.gateCounts[i] == gate.Occurrence {
			index := i
			f.event(OrderingEvent{Kind: "gate_eligible", JobID: job, Cell: cell, NodePath: path, ResponseIndex: &index})
		}
	}
}

func (f *runtimeFixture) awaitChild(ctx context.Context, key jobdb.JobKey, parent, path string) error {
	f.mu.Lock()
	f.waiting[key.JobId] = true
	f.mu.Unlock()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspection, err := f.ctl.InspectJob(ctx, key)
		if err != nil {
			return fmt.Errorf("await child %s: %w", key.JobId, err)
		}
		if inspection.Terminal {
			f.mu.Lock()
			delete(f.waiting, key.JobId)
			f.event(OrderingEvent{Kind: "child_terminal", JobID: key.JobId, ParentJobID: parent, NodePath: path, Status: inspection.Status})
			f.mu.Unlock()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("await child %s: %w", key.JobId, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (f *runtimeFixture) unresolved(err error) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	details := []string{err.Error()}
	for i, response := range f.c.Runtime.Responses {
		if gate := response.AfterCall; gate != nil && !f.responses[i] {
			details = append(details, fmt.Sprintf("unresolved response %d after_call node_path=%q cell=%q occurrence=%d entries=%d", i, gate.NodePath, gate.Cell, gate.Occurrence, f.gateCounts[i]))
		}
	}
	for id := range f.waiting {
		details = append(details, "unfinished child "+id)
	}
	return strings.Join(details, "; ")
}
