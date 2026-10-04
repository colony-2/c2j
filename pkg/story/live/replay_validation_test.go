package live

import (
	"context"
	"errors"
	"fmt"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"log/slog"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type scriptedStoryEngine struct {
	jobworkflow.Engine
	run        jobdb.GetJobRunResponse
	historyErr error
	replay     func(jobworkflow.ReplayRunRequest) (jobdb.JobData, error)
}

func (e *scriptedStoryEngine) GetJobRun(_ context.Context, req jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	return e.run, e.historyErr
}
func (e *scriptedStoryEngine) ReplayJobRun(_ context.Context, req jobworkflow.ReplayRunRequest) (jobdb.JobData, error) {
	return e.replay(req)
}

func TestStoryReplayPropagatesUnexpectedErrors(t *testing.T) {
	readErr := errors.New("chapter store unavailable")
	for _, cause := range []error{readErr, fmt.Errorf("decode chapter: %w", readErr), jobdb.ErrWorkflowNotDeterministic, context.Canceled, context.DeadlineExceeded, jobdb.ErrJobNotFound} {
		t.Run(cause.Error(), func(t *testing.T) {
			engine := &scriptedStoryEngine{replay: func(jobworkflow.ReplayRunRequest) (jobdb.JobData, error) { return nil, cause }}
			_, err := BuildJobRunStory(context.Background(), engine, jobdb.JobKey{TenantId: "tenant", JobId: "job"}, nil, nil)
			require.ErrorIs(t, err, cause)
		})
	}
	engine := &scriptedStoryEngine{historyErr: readErr, replay: func(jobworkflow.ReplayRunRequest) (jobdb.JobData, error) {
		t.Fatal("replayed without readable history")
		return nil, nil
	}}
	_, err := BuildJobRunStory(context.Background(), engine, jobdb.JobKey{TenantId: "tenant", JobId: "job"}, nil, nil)
	require.ErrorIs(t, err, readErr)
}

func TestStoryReplayChecksEveryPersistedAttempt(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	failure := &jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "recorded failure"}}
	var run jobdb.GetJobRunResponse
	run.Job.JobKey = key
	for j := 1; j <= 3; j++ {
		job := jobdb.JobAttempt{Attempt: j, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusFailed, Error: &jobdb.TaskError{Message: failure.Error()}}}
		task := jobdb.TaskRun{TaskType: compiler.RootSourceResolutionTaskType}
		for a := 1; a <= 3; a++ {
			task.Attempts = append(task.Attempts, jobdb.TaskAttempt{Ordinal: int64((j-1)*4 + a), Attempt: a, State: jobdb.TaskAttemptStateFailed})
		}
		job.Tasks = []jobdb.TaskRun{task}
		run.Attempts = append(run.Attempts, job)
	}
	for _, missing := range []string{"none", "all tasks", "first job", "last job end", "middle task start", "middle task end"} {
		t.Run(missing, func(t *testing.T) {
			engine := &scriptedStoryEngine{run: run, replay: func(req jobworkflow.ReplayRunRequest) (jobdb.JobData, error) {
				for _, j := range run.Attempts {
					if missing == "first job" && j.Attempt == 1 {
						continue
					}
					req.Observer.OnJobStart(jobworkflow.JobStartEvent{JobKey: key, AttemptNumber: j.Attempt, At: time.Unix(int64(j.Attempt), 0)})
					if missing != "all tasks" {
						for _, a := range j.Tasks[0].Attempts {
							if missing != "middle task start" || a.Ordinal != 6 {
								req.Observer.OnTaskStart(jobworkflow.TaskStartEvent{JobKey: key, TaskType: compiler.RootSourceResolutionTaskType, Ordinal: a.Ordinal, AttemptNumber: a.Attempt})
							}
							if missing != "middle task end" || a.Ordinal != 6 {
								req.Observer.OnTaskEnd(jobworkflow.TaskEndEvent{JobKey: key, TaskType: compiler.RootSourceResolutionTaskType, Ordinal: a.Ordinal, AttemptNumber: a.Attempt, Err: failure})
							}
						}
					}
					if missing != "last job end" || j.Attempt != 3 {
						req.Observer.OnJobEnd(jobworkflow.JobEndEvent{JobKey: key, AttemptNumber: j.Attempt, Err: failure, At: time.Unix(int64(j.Attempt+1), 0)})
					}
				}
				return nil, failure
			}}
			st, err := BuildJobRunStory(context.Background(), engine, key, nil, nil)
			if missing == "none" {
				require.NoError(t, err)
				require.Equal(t, WorkflowStatusFailed, st.Status)
				require.Len(t, st.Root.PastAttempts, 2)
				return
			}
			require.ErrorIs(t, err, ErrIncompleteReplay)
			require.ErrorIs(t, err, failure, "preserve the underlying error as well as the integrity failure")
		})
	}
}

func TestStoryReplayCacheMissRequiresAllRecordedEvents(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	miss := jobworkflow.ReplayCacheMissError{JobKey: key, Ordinal: 2, Attempt: 1, Reason: jobworkflow.ReplayCacheMissTaskResultMissing}
	for _, persisted := range []bool{false, true} {
		t.Run(fmt.Sprint(persisted), func(t *testing.T) {
			engine := &scriptedStoryEngine{replay: func(jobworkflow.ReplayRunRequest) (jobdb.JobData, error) { return nil, miss }}
			if persisted {
				engine.run.Attempts = []jobdb.JobAttempt{{Attempt: 1, Tasks: []jobdb.TaskRun{{TaskType: "task", Attempts: []jobdb.TaskAttempt{{Ordinal: 1, Attempt: 1, State: jobdb.TaskAttemptStateSucceeded}}}}}}
			}
			st, err := BuildJobRunStory(context.Background(), engine, key, nil, nil)
			if persisted {
				require.ErrorIs(t, err, ErrIncompleteReplay)
			} else {
				require.NoError(t, err)
				require.Equal(t, WorkflowStatusRunning, st.Status)
			}
		})
	}
}

func TestStoryReplaySuccessfulReturnCannotHideMissingHistory(t *testing.T) {
	engine := &scriptedStoryEngine{run: jobdb.GetJobRunResponse{Attempts: []jobdb.JobAttempt{{Attempt: 1, Outcome: jobdb.TaskOutcome{Status: jobdb.TaskOutcomeStatusSucceeded}}}}, replay: func(jobworkflow.ReplayRunRequest) (jobdb.JobData, error) { return nil, nil }}
	_, err := BuildJobRunStory(context.Background(), engine, jobdb.JobKey{TenantId: "tenant", JobId: "job"}, nil, nil)
	require.ErrorIs(t, err, ErrIncompleteReplay)
}

type loadErrorReplayContext struct {
	jobworkflow.JobContext
	observer jobworkflow.ReplayObserver
	output   jobdb.TaskData
}

func (*loadErrorReplayContext) GetJobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "tenant", JobId: "job"}
}
func (*loadErrorReplayContext) Logger() *slog.Logger { return slog.Default() }
func (c *loadErrorReplayContext) DoTask(_ jobdb.RunPolicy, task string, input jobdb.TaskData) (jobdb.TaskData, error) {
	c.observer.OnTaskStart(jobworkflow.TaskStartEvent{TaskType: task, Ordinal: 1, AttemptNumber: 1, Input: input})
	c.observer.OnTaskEnd(jobworkflow.TaskEndEvent{TaskType: task, Ordinal: 1, AttemptNumber: 1, Output: c.output})
	return c.output, nil
}

func TestStoryReplayRetainsRecipeLoadErrorWhenEngineReportsMismatch(t *testing.T) {
	for _, source := range []struct{ name, yaml, want string }{
		{"invalid yaml", "sequence: [", "parse resolved recipe YAML"},
		{"unregistered op", "id: test\nsequence:\n - op: intentionally_unregistered_story_op\n", "unknown op"},
	} {
		t.Run(source.name, func(t *testing.T) {
			engine := &scriptedStoryEngine{replay: func(req jobworkflow.ReplayRunRequest) (jobdb.JobData, error) {
				start, err := jobdb.NewTaskData(workflowctl.StartJob{TenantId: "tenant", RecipeName: "test"})
				require.NoError(t, err)
				resolved, err := jobdb.NewTaskData(compiler.ResolvedRecipeSource{RecipeSourceResolution: compiler.RecipeSourceResolution{SourceKind: compiler.RecipeSourceKindGit}, RecipeYAML: source.yaml})
				require.NoError(t, err)
				req.Observer.OnJobStart(jobworkflow.JobStartEvent{JobKey: req.JobKey, AttemptNumber: 1, Input: start})
				var loadErr error
				require.NotPanics(t, func() {
					_, loadErr = req.JobWorker.Run(&loadErrorReplayContext{observer: req.Observer, output: resolved}, start)
				})
				require.ErrorContains(t, loadErr, source.want)
				// The engine encounters a task chapter where it now expects a job outcome.
				return nil, fmt.Errorf("unexpected chapter type at ordinal 2: %w", jobdb.ErrWorkflowNotDeterministic)
			}}
			_, err := BuildJobRunStory(context.Background(), engine, jobdb.JobKey{TenantId: "tenant", JobId: "job"}, nil, nil)
			require.ErrorIs(t, err, jobdb.ErrWorkflowNotDeterministic)
			require.ErrorContains(t, err, source.want)
		})
	}
}
