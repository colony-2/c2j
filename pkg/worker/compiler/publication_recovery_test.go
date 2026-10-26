package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	coretask "github.com/colony-2/c2j/pkg/task"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type publicationFaultWorker struct {
	jobworkflow.TaskWorker
	after func(jobdb.TaskData) error
}

func (w publicationFaultWorker) Run(ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(ctx.Context(), ctx, input)
}
func (w publicationFaultWorker) RunTask(local context.Context, ctx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	result, err := coretask.RunLocal(w.TaskWorker, local, ctx, input)
	if err == nil {
		if err = w.after(result); err != nil {
			// Simulate losing the entire outcome, including its workspace pack,
			// after the operation and persistence have already succeeded.
			return nil, err
		}
	}
	return result, err
}

// Use real task persistence, restore and Git transport. Every attempt receives a
// new worktree; the failed attempt's worktree is also explicitly destroyed.
func TestSquashRebaseMergeLostTaskOutcome(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, scenario := range []struct{ step, remote string }{
			{"squash", "unchanged"},
			{"rebase", "unchanged"},
			{"merge", "unchanged"},
			{"merge", "advanced"},
			{"merge", "diverged"},
		} {
			t.Run(fmt.Sprintf("empty=%t/%s/%s", empty, scenario.step, scenario.remote), func(t *testing.T) {
				ctx := context.Background()
				root := t.TempDir()
				source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
				initial := workspaceRepo(t, source, "source")
				if empty {
					require.NoError(t, runGit(root, "git", "init", "--bare", remote))
				} else {
					require.NoError(t, runGit(root, "git", "clone", "--bare", source, remote))
				}
				require.NoError(t, common.EnsureGitIdentity(ctx, remote))
				advance := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "publication_advance"}, func(_ coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
					if empty {
						return nil, nil
					}
					hash, err := advancePublicationTarget(ctx, remote, initial, "upstream")
					return map[string]any{"hash": hash}, err
				})
				workspaceTestWorker(t, squashrebasemerge.GetOp(), advance)
				registry, err := workerops.NewActivityRegistry()
				require.NoError(t, err)
				var mu sync.Mutex
				calls := map[string]int{}
				paths := map[string][]string{}
				for name, reg := range registry.GetAll() {
					if reg.Metadata.Type != "squashrebasemerge" {
						continue
					}
					invoke, step := reg.Step.Invoke, reg.Step.Name
					reg.Step.Invoke = func(d coreops.OpDependencies, c context.Context, in map[string]any) (map[string]any, error) {
						mu.Lock()
						calls[step]++
						paths[step] = append(paths[step], d.WorktreePath())
						mu.Unlock()
						return invoke(d, c, in)
					}
					registry.UpdateRegistration(name, reg)
				}
				ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
				require.NoError(t, err)
				faultTask := "squashrebasemerge:" + scenario.step
				injected := false
				published, competing := "", ""
				ws.TaskWorkers[faultTask] = publicationFaultWorker{TaskWorker: ws.TaskWorkers[faultTask], after: func(result jobdb.TaskData) error {
					mu.Lock()
					defer mu.Unlock()
					if injected {
						return nil
					}
					injected = true
					if scenario.step == "merge" {
						raw, err := result.GetData()
						if err != nil {
							return err
						}
						out, err := decodeTaskOutput(raw)
						if err != nil {
							return err
						}
						published = out.Activity.OpOutput["merged_hash"].(string)
						actual, err := common.GetCommitHash(ctx, remote, "refs/heads/main")
						if err != nil || actual != published {
							return fmt.Errorf("push did not precede injected failure: %s, %v", actual, err)
						}
						if scenario.remote != "unchanged" {
							parent := published
							if scenario.remote == "diverged" {
								parent = "" // Independent history must never be overwritten.
							}
							competing, err = advancePublicationTarget(ctx, remote, parent, "other writer")
							if err != nil {
								return err
							}
						}
					}
					if err := os.RemoveAll(paths[scenario.step][0]); err != nil {
						return err
					}
					return fmt.Errorf("injected loss after successful %s", scenario.step)
				}}
				yaml := `id: workspace-test
sequence:
 - id: first
   op: workspace_probe
   inputs: {write: first.txt}
 - id: second
   op: workspace_probe
   inputs: {write: second.txt}
 - id: advance
   op: publication_advance
 - id: publish
   op: squashrebasemerge
   retry: {maximum_attempts: 2, initial_interval: 1ms}
   inputs: {commit_message: "Prepared publication"}
 - id: restored
   op: workspace_probe
   inputs: {read: first.txt}
outputs:
 publication: "${{ sequence.publish.outputs }}"
 restored: "${{ sequence.restored.outputs.head }}"
`
				job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: remote, BaseRef: "main"}}
				if empty {
					yaml = strings.Replace(yaml, "id: workspace-test\n", fmt.Sprintf("id: workspace-test\nworkspace: {cell: %q, ref: main, initial: empty}\n", remote), 1)
					job.GitBase.BaseRepo = source
				}
				rec, err := recipe.LoadRecipeFromString([]byte(yaml))
				require.NoError(t, err)
				eng := newToyEngineWithWorkSet(t, "publication-tests", ws, nil)
				keyRef, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "publication-tests", RecipeName: "workspace-test", JobContext: job, GitRef: "main"}, eng, *rec)
				key := keyRef.Key()
				require.NoError(t, err)
				require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 15*time.Second, key, eng))
				result, resultErr := swfutil.JobResult(ctx, eng, key)
				mu.Lock()
				defer mu.Unlock()
				require.True(t, injected)
				for _, step := range []string{"squash", "rebase", "merge"} {
					want := 1
					if step == scenario.step {
						want = 2
					}
					require.Equal(t, want, calls[step], "step %s", step)
				}
				require.NotEqual(t, paths[scenario.step][0], paths[scenario.step][1])
				remoteHead, err := common.GetCommitHash(ctx, remote, "refs/heads/main")
				require.NoError(t, err)
				if scenario.remote == "diverged" {
					require.Error(t, resultErr)
					require.Equal(t, competing, remoteHead)
					return
				}
				require.NoError(t, resultErr)
				raw, err := result.GetData()
				require.NoError(t, err)
				var output struct {
					Publication squashrebasemerge.SquashRebaseMergeOutput `json:"publication"`
					Restored    string                                    `json:"restored"`
				}
				require.NoError(t, json.Unmarshal(raw, &output))
				candidate := output.Publication.MergedHash
				require.Equal(t, candidate, output.Restored)
				if published != "" {
					require.Equal(t, published, candidate, "retry must publish the identical saved commit")
				}
				if competing != "" {
					require.Equal(t, competing, remoteHead)
				} else {
					require.Equal(t, candidate, remoteHead)
				}
				count, err := common.ExecuteGitCommand(ctx, remote, "rev-list", "--count", candidate)
				require.NoError(t, err)
				wantCount := "3\n" // Initial, upstream advance, one prepared commit.
				if empty {
					wantCount = "1\n"
				}
				require.Equal(t, wantCount, string(count))
				// Recorded replay must work without a reachable repository.
				require.NoError(t, os.RemoveAll(remote))
				replay, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
				require.NoError(t, err)
				replayRaw, err := replay.GetData()
				require.NoError(t, err)
				require.JSONEq(t, string(raw), string(replayRaw))
			})
		}
	}
}

func advancePublicationTarget(ctx context.Context, remote, parent, message string) (string, error) {
	tree, err := common.GetCommitHash(ctx, remote, "refs/heads/main^{tree}")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", message}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	out, err := common.ExecuteGitCommand(ctx, remote, args...)
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(out))
	_, err = common.ExecuteGitCommand(ctx, remote, "update-ref", "refs/heads/main", hash)
	return hash, err
}

type publicationStorageFaultRuntime struct {
	jobdb.WorkflowRuntime
	lease jobdb.ExecutionLease
	fired bool
}

func (r *publicationStorageFaultRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	lease, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	r.lease = lease
	return lease, err
}

func (r *publicationStorageFaultRuntime) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if body, ok := req.Chapter.Body.(jobdb.ExecutionChapter); ok && !r.fired {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if record.Kind == "result" && strings.Contains(record.ID, `task:"squashrebasemerge:merge"`) {
			r.fired = true
			// Drop the actual JobDB outcome write and release the worker lease.
			// Recovery must run the same incomplete task from its recorded input.
			if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}); err != nil {
				return err
			}
			return fmt.Errorf("injected task outcome storage failure after push")
		}
	}
	return r.WorkflowRuntime.PutChapter(ctx, req)
}

func TestSquashRebaseMergeRecoversMissingOutcomeOnNewWorker(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
			workspaceRepo(t, source, "source")
			if empty {
				require.NoError(t, runGit(root, "git", "init", "--bare", remote))
			} else {
				require.NoError(t, runGit(root, "git", "clone", "--bare", source, remote))
			}
			require.NoError(t, common.EnsureGitIdentity(ctx, remote))
			workspaceTestWorker(t, squashrebasemerge.GetOp())
			calls := map[string]int{}
			var mu sync.Mutex
			newWorker := func() *jobworkflow.WorkSet {
				reg, err := workerops.NewActivityRegistry()
				require.NoError(t, err)
				ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), reg)
				require.NoError(t, err)
				for name, worker := range ws.TaskWorkers {
					if strings.HasPrefix(name, "squashrebasemerge:") {
						ws.TaskWorkers[name] = publicationFaultWorker{TaskWorker: worker, after: func(jobdb.TaskData) error {
							mu.Lock()
							defer mu.Unlock()
							calls[name]++
							return nil
						}}
					}
				}
				return ws
			}
			base := toyruntime.New()
			rt := &publicationStorageFaultRuntime{WorkflowRuntime: base}
			first := newWorker()
			eng, err := jobworkflow.NewEngineBuilder().WithRuntime(base).WithWorkerTenantId("recovery").PlusWorkers(first.JobWorker).BuildEngine()
			require.NoError(t, err)
			eng = jobdbschema.WorkflowEngine{Engine: eng, Registry: base}
			yaml := `id: recover-publication
sequence:
 - id: write
   op: workspace_probe
   inputs: {write: feature.txt}
 - id: publish
   op: squashrebasemerge
outputs:
 merged: '${{ sequence.publish.outputs.merged_hash }}'
`
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: remote, BaseRef: "main"}}
			if empty {
				yaml = strings.Replace(yaml, "id: recover-publication\n", fmt.Sprintf("id: recover-publication\nworkspace: {cell: %q, ref: main, initial: empty}\n", remote), 1)
				job.GitBase.BaseRepo = source
			}
			rec, err := recipe.LoadRecipeFromString([]byte(yaml))
			require.NoError(t, err)
			keyRef, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "recovery", RecipeName: "recover-publication", JobContext: job, GitRef: "main"}, eng, *rec)
			key := keyRef.Key()
			require.NoError(t, err)
			run := func(ws *jobworkflow.WorkSet, id string) (jobworkflow.JobRunOutcome, error) {
				guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
				var workers []jobworkflow.TaskWorker
				for _, worker := range ws.TaskWorkers {
					workers = append(workers, guard.WrapTaskWorker(worker))
				}
				h, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: id, LeaseDuration: time.Minute})
				if err != nil {
					return jobworkflow.JobRunOutcome{}, err
				}
				return h.Run(nil)
			}
			_, err = run(first, "interrupted")
			require.Error(t, err)
			require.True(t, rt.fired)
			published, err := common.GetCommitHash(ctx, remote, "refs/heads/main")
			require.NoError(t, err)
			advanced, err := advancePublicationTarget(ctx, remote, published, "later writer")
			require.NoError(t, err)
			out, err := run(newWorker(), "replacement")
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			result, err := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, err)
			raw, err := result.GetData()
			require.NoError(t, err)
			var output map[string]any
			require.NoError(t, json.Unmarshal(raw, &output))
			require.Equal(t, published, output["merged"])
			actual, err := common.GetCommitHash(ctx, remote, "refs/heads/main")
			require.NoError(t, err)
			require.Equal(t, advanced, actual)
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, map[string]int{"squashrebasemerge:squash": 1, "squashrebasemerge:rebase": 1, "squashrebasemerge:merge": 2}, calls)
		})
	}
}
