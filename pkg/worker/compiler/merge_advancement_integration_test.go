package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type mergeCaptureInput struct {
	Failure map[string]any `json:"failure"`
}

// Stop after the real task error is durable, before recipe catch consumes it.
type mergeOutcomeInterruption struct {
	jobdb.WorkflowRuntime
	lease  jobdb.ExecutionLease
	fired  bool
	cancel context.CancelFunc
}

func (r *mergeOutcomeInterruption) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	r.lease = lease
	return lease, err
}

func (r *mergeOutcomeInterruption) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if err := r.WorkflowRuntime.PutChapter(ctx, req); err != nil {
		return err
	}
	if body, ok := req.Chapter.Body.(jobdb.ExecutionChapter); ok && !r.fired {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if record.Kind == "result" && strings.Contains(record.ID, `task:"merge:merge"`) {
			r.fired = true
			if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}); err != nil {
				return err
			}
			r.cancel()
			return fmt.Errorf("injected worker loss after durable merge error")
		}
	}
	return nil
}

func TestNativeMergeFailureCatchAndResume(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, scenario := range []string{"advanced", "resume", "other node", "hook", "squash"} {
			t.Run(backend+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
				baseHash := workspaceRepo(t, source, "source")
				require.NoError(t, runGit(root, "git", "clone", "--bare", source, remote))
				require.NoError(t, common.EnsureGitIdentity(ctx, remote))
				var advanced string
				advance := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "merge_test_advance"}, func(_ coreops.OpDependencies, c context.Context, _ struct{}) (map[string]any, error) {
					if scenario == "hook" {
						return nil, os.WriteFile(filepath.Join(remote, "hooks/pre-receive"), []byte("#!/bin/sh\necho 'protected branch' >&2\nexit 1\n"), 0700)
					}
					var err error
					advanced, err = advancePublicationTarget(c, remote, baseHash, "concurrent publication")
					return nil, err
				})
				var captures atomic.Int32
				capture := coreops.NewActivityMappedOpV2[mergeCaptureInput, map[string]any](coreops.OpMetadata{Type: "merge_test_capture"}, func(d coreops.OpDependencies, c context.Context, input mergeCaptureInput) (map[string]any, error) {
					captures.Add(1)
					head, err := common.GetCommitHash(c, d.WorktreePath(), "HEAD")
					output := map[string]any{"failure": input.Failure, "restored_head": head, "restored_base": d.GitContext().ResolvedBaseHash}
					_, readErr := os.ReadFile(filepath.Join(d.WorktreePath(), "feature.txt"))
					if readErr != nil {
						return nil, readErr
					}
					return output, err
				})
				workspaceTestWorker(t, squashrebasemerge.GetMergeOp(), squashrebasemerge.GetSquashOp(), advance, capture)
				var mergeCalls atomic.Int32
				newWorker := func() *jobworkflow.WorkSet {
					reg, err := workerops.NewActivityRegistry()
					require.NoError(t, err)
					for name, entry := range reg.GetAll() {
						if entry.Metadata.Type == "merge" {
							invoke := entry.Step.Invoke
							entry.Step.Invoke = func(d coreops.OpDependencies, c context.Context, input map[string]any) (map[string]any, error) {
								mergeCalls.Add(1)
								return invoke(d, c, input)
							}
							reg.UpdateRegistration(name, entry)
						}
					}
					ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), reg)
					require.NoError(t, err)
					return ws
				}
				yaml := `id: merge-catch
sequence:
  - id: write
    op: workspace_probe
    inputs: {write: feature.txt}
  - id: advance
    op: merge_test_advance
  - id: block
    state:
      initial: publish
      states:
        publish:
          sequence:
            - id: prepare
              op: squash
            - id: nested
              sequence:
                - id: merge_reviewed_candidate
                  op: merge
                  retry: {maximum_attempts: 3, initial_interval: 1ms}
          catch:
            - id: advanced
              when: failure.kind == 'task_error' && failure.code == 'MERGE_UPSTREAM_ADVANCED' && failure.node.op == 'merge' && failure.node.id == 'merge_reviewed_candidate'
              to: recovered
              payload:
                failure: '${{ failure }}'
        recovered:
          op: merge_test_capture
          inputs:
            failure: '${{ transition.payload.failure }}'
    outputs:
      captured: '${{ states.recovered.outputs }}'
outputs:
  captured: '${{ sequence.block.outputs.captured }}'
`
				if scenario == "other node" {
					yaml = strings.Replace(yaml, "id: merge_reviewed_candidate\n", "id: other_merge\n", 1)
				}
				if scenario == "squash" {
					yaml = strings.Replace(yaml, "op: squash\n", "op: squash\n              inputs: {base_hash: "+strings.Repeat("a", 40)+"}\n", 1)
				}
				_, err := recipe.LoadRecipeFromString([]byte(yaml))
				require.NoError(t, err)
				rt := newTimeoutReproRuntime(t, backend)
				fault := &mergeOutcomeInterruption{WorkflowRuntime: rt}
				first := newWorker()
				job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: remote, BaseRef: "main"}}
				data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "merge-tests", RecipeName: "merge-catch", JobContext: job, GitRef: "main"}, jobdb.NewArtifactFromBytes("merge-catch"+starter.RecipeArtifactSuffix, []byte(yaml)))
				handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "merge-tests", JobType: "recipe", Data: data, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
				require.NoError(t, err)
				key := handle.JobKey
				run := func(runtime jobdb.WorkflowRuntime, ws *jobworkflow.WorkSet) (jobworkflow.JobRunOutcome, error) {
					runContext := ctx
					if runtime == fault {
						runContext, fault.cancel = context.WithCancel(ctx)
						defer fault.cancel()
					}
					guard := executionruntime.New(runtime, execution.Allocation{SchemaVersion: 1})
					guard.Issuer = permissions.JobDBIssuer{}
					var tasks []jobworkflow.TaskWorker
					for _, task := range ws.TaskWorkers {
						tasks = append(tasks, guard.WrapTaskWorker(task))
					}
					runnable, err := jobworkflow.GetJobForRun(runContext, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "merge-test", LeaseDuration: time.Minute})
					if err != nil {
						return jobworkflow.JobRunOutcome{}, err
					}
					return runnable.Run(nil)
				}
				if scenario == "resume" {
					_, err = run(fault, first)
					require.Error(t, err)
					require.True(t, fault.fired)
					require.Zero(t, captures.Load())
				}
				out, err := run(rt, newWorker())
				require.NoError(t, err)
				if scenario == "other node" || scenario == "hook" || scenario == "squash" {
					require.Equal(t, jobworkflow.JobRunFailed, out.Status)
					require.Zero(t, captures.Load())
					failure, ok := failureFromError(out.JobError)
					require.True(t, ok, "%v", out.JobError)
					if scenario == "other node" {
						require.Equal(t, "other_merge", failure.Node.ID)
						require.Equal(t, squashrebasemerge.MergeUpstreamAdvancedCode, failure.Code)
					}
					if scenario == "hook" {
						require.Equal(t, "merge", failure.Node.Op)
						require.NotEqual(t, squashrebasemerge.MergeUpstreamAdvancedCode, failure.Code)
						require.Contains(t, failure.Message, "hook declined")
					}
					if scenario == "squash" {
						require.Equal(t, "squash", failure.Node.Op)
						require.Zero(t, mergeCalls.Load())
					}
					return
				}
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.EqualValues(t, 1, mergeCalls.Load(), "catch and resume must not repeat the merge task")
				require.EqualValues(t, 1, captures.Load())
				eng, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(first.JobWorker).BuildEngine()
				require.NoError(t, err)
				result, err := swfutil.JobResult(ctx, eng, key)
				require.NoError(t, err)
				raw, err := result.GetData()
				require.NoError(t, err)
				var output struct {
					Captured struct {
						Failure recipe.RuntimeFailure `json:"failure"`
						Head    string                `json:"restored_head"`
						Base    string                `json:"restored_base"`
					} `json:"captured"`
				}
				require.NoError(t, json.Unmarshal(raw, &output))
				failure := output.Captured.Failure
				require.Equal(t, recipe.FailureKindTaskError, failure.Kind)
				require.Equal(t, squashrebasemerge.MergeUpstreamAdvancedCode, failure.Code)
				require.Equal(t, "merge_reviewed_candidate", failure.Node.ID)
				require.Equal(t, "merge", failure.Node.Op)
				require.Contains(t, failure.Node.Path, "nested/merge_reviewed_candidate")
				require.Equal(t, map[string]any{"code": squashrebasemerge.MergeUpstreamAdvancedCode, "candidate_hash": output.Captured.Head, "prepared_base_hash": baseHash, "upstream_hash": advanced, "target_branch": "refs/heads/main", "phase": "preflight"}, failure.Attrs)
				require.Equal(t, baseHash, output.Captured.Base)
				// Read-only replay must use persisted errors even without Git access.
				require.NoError(t, os.RemoveAll(remote))
				replayed, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				replayRaw, err := replayed.GetData()
				require.NoError(t, err)
				require.JSONEq(t, string(raw), string(replayRaw))
				require.EqualValues(t, 1, mergeCalls.Load())
			})
		}
	}
}
