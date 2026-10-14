package swfutil

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ExecutionTask is a logical task and its committed attempts. Ordinals identify
// stored results only; InvocationID distinguishes repeats and block retries.
type ExecutionTask struct {
	InvocationID string
	jobdb.TaskRun
}

// ExecutionTasks projects versioned records for diagnostic consumers. It does
// not construct worker inputs or participate in execution/replay.
func ExecutionTasks(run jobdb.GetJobRunResponse) ([]ExecutionTask, error) {
	var tasks []ExecutionTask
	byID := map[string]int{}
	for _, entry := range run.Execution {
		r := entry.Record
		if r.Kind == "task" {
			var descriptor struct {
				Call struct {
					TaskType string `json:"task_type"`
				} `json:"call"`
			}
			if err := json.Unmarshal(r.Descriptor, &descriptor); err != nil {
				return nil, err
			}
			byID[r.ID] = len(tasks)
			tasks = append(tasks, ExecutionTask{InvocationID: r.ID, TaskRun: jobdb.TaskRun{TaskRunID: r.ID, TaskType: descriptor.Call.TaskType}})
		}
		if r.Kind != "result" {
			continue
		}
		split := strings.LastIndex(r.ID, "/attempt:")
		if split < 0 {
			continue
		}
		index, ok := byID[r.ID[:split]]
		if !ok {
			continue
		}
		attempt, err := strconv.Atoi(r.ID[split+len("/attempt:"):])
		if err != nil {
			return nil, fmt.Errorf("task attempt %s: %w", r.ID, err)
		}
		a := jobdb.TaskAttempt{Ordinal: entry.Ordinal, Attempt: attempt, CreatedAt: r.At, State: jobdb.TaskAttemptStateSucceeded}
		a.Input = executionTaskIO(r.Input)
		a.Output = executionTaskIO(r.Output)
		if r.ErrorKind != "" {
			a.State = jobdb.TaskAttemptStateFailed
		}
		tasks[index].Attempts = append(tasks[index].Attempts, a)
	}
	return tasks, nil
}
func executionTaskIO(s *jobdb.ExecutionSnapshot) *jobdb.TaskIO {
	if s == nil {
		return nil
	}
	out := &jobdb.TaskIO{Data: s.Data}
	for _, a := range s.Artifacts {
		size := int64(len(a.Data))
		if a.Key != nil {
			size = a.Key.SizeBytes
		}
		out.Artifacts = append(out.Artifacts, jobdb.ArtifactInfo{Name: a.Name, SizeBytes: size, Key: a.Key})
	}
	return out
}
