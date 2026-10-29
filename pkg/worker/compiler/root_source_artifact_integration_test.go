package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Record the previous writer's representation, then replay it with today's
// reader. No rewriting of the recorded application data is involved.
type inlineRootSourceWorker struct{ jobworkflow.TaskWorker }

func (w inlineRootSourceWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := w.TaskWorker.Run(tc, in)
	if err != nil {
		return nil, err
	}
	source, err := ParseResolvedRecipeSourceTaskData(out)
	if err != nil {
		return nil, err
	}
	raw, err := source.RecipeYAMLBytes(tc.Context())
	if err != nil {
		return nil, err
	}
	source.RecipeYAML, source.RecipeYAMLArtifact = string(raw), ""
	return jobdb.NewTaskData(source)
}

type rootSourceInterruption struct {
	jobdb.WorkflowRuntime
	lease  jobdb.ExecutionLease
	cancel context.CancelFunc
	fired  bool
}

func (r *rootSourceInterruption) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	r.lease = lease
	return lease, err
}

func (r *rootSourceInterruption) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if err := r.WorkflowRuntime.PutChapter(ctx, req); err != nil {
		return err
	}
	if body, ok := req.Chapter.Body.(jobdb.ExecutionChapter); ok && !r.fired {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if record.Kind == "result" && record.Output != nil && strings.Contains(string(record.Output.Data), "submitted_selector") {
			r.fired = true
			if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}); err != nil {
				return err
			}
			r.cancel()
			return fmt.Errorf("injected worker loss after durable source result")
		}
	}
	return nil
}

func TestResolvedRecipeArtifactDurableReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", backend, legacy), func(t *testing.T) {
				ctx := context.Background()
				rt := newTimeoutReproRuntime(t, backend)
				size := 1_300_000
				if legacy {
					size = 1000
				}
				yaml := []byte("id: root\ndesc: " + strings.Repeat("x", size) + "\nsequence: []\noutputs: {ok: true}\n")
				available := true
				loads := 0
				resolver := &conventionResolver{
					resolve: func(selector string) (RecipeSourceResolution, error) {
						if !available {
							return RecipeSourceResolution{}, fmt.Errorf("original source is gone")
						}
						return RecipeSourceResolution{SourceKind: RecipeSourceKindServerRef, SubmittedSelector: selector, ResolvedSelector: selector + "@pinned"}, nil
					},
					load: func(RecipeSourceResolution) ([]byte, error) {
						loads++
						if !available {
							return nil, fmt.Errorf("original source is gone")
						}
						return yaml, nil
					},
				}
				data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "recipe-artifacts", RecipeName: "root"})
				handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "recipe-artifacts", JobType: "recipe", Data: data, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
				require.NoError(t, err)
				worker := NewRecipeJobWorker(RecipeJobWorkerOptions{RootSourceResolver: resolver})
				sourceTask := newRootSourceResolutionTaskWorker(resolver)
				if legacy {
					sourceTask = inlineRootSourceWorker{sourceTask}
				}
				// Stop immediately after the source result is durable. A fresh
				// worker must recover both YAML representations without resolving.
				firstCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				interrupted := &rootSourceInterruption{WorkflowRuntime: rt, cancel: cancel}
				first, err := jobworkflow.GetJobForRun(firstCtx, interrupted, jobworkflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{sourceTask}, WorkerID: "first-worker", LeaseDuration: time.Minute})
				require.NoError(t, err)
				_, err = first.Run(nil)
				require.Error(t, err)
				require.True(t, interrupted.fired)
				available = false
				worker = NewRecipeJobWorker(RecipeJobWorkerOptions{RootSourceResolver: resolver})
				out := probeRun(t, rt, handle.JobKey, worker, sourceTask)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, 1, loads, "artifact-backed roots must not invoke root-source-load")
				chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				found := false
				for _, ch := range chapters {
					require.NoError(t, jobdb.ValidateChapterApplicationPayload(ctx, ch, 0), "default 256 KiB application limit")
					body, ok := ch.Body.(jobdb.ExecutionChapter)
					if !ok {
						continue
					}
					record, err := jobdb.DecodeExecutionRecord(body.Record)
					require.NoError(t, err)
					if record.Output == nil || !strings.Contains(string(record.Output.Data), "submitted_selector") {
						continue
					}
					found = true
					var value map[string]any
					require.NoError(t, json.Unmarshal(record.Output.Data, &value))
					if legacy {
						require.Contains(t, value, "recipe_yaml")
					} else {
						require.NotContains(t, value, "recipe_yaml")
						require.Less(t, len(record.Output.Data), 1024)
						require.Equal(t, ResolvedRecipeYAMLArtifactName, value["recipe_yaml_artifact"])
						require.NotEmpty(t, record.Output.Artifacts)
						require.NotEmpty(t, ch.Artifacts)
						require.Greater(t, ch.Artifacts[0].Size, int64(1_240_000))
					}
				}
				require.True(t, found)
				available = false
				restarted := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true, RootSourceResolver: resolver})
				require.NoError(t, probeReplay(t, rt, handle.JobKey, restarted))
				require.Equal(t, 1, loads, "replay must use the stored snapshot with the source unavailable")
				after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				require.Equal(t, chapters, after, "read-only replay must not rewrite legacy or artifact-backed history")
				run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				var scope string
				for _, event := range run.Execution {
					if event.Record.Kind == "scope" && strings.HasSuffix(event.Record.ID, `scope:"sequence:root"#1`) {
						scope = event.Record.ID
						break
					}
				}
				require.NotEmpty(t, scope)
				continuation, err := rt.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: handle.JobKey, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scope}}})
				require.NoError(t, err)
				continued := probeRun(t, rt, continuation.JobKey, worker, sourceTask)
				require.Equal(t, jobworkflow.JobRunCompleted, continued.Status, "%v", continued.JobError)
				require.NoError(t, probeReplay(t, rt, continuation.JobKey, restarted))
				require.Equal(t, 1, loads, "continuation must read source through the inherited artifact key")
			})
		}
	}
}
