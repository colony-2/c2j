package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/starter"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Lose the worker after committing an outcome but before the compiler consumes
// it. The replacement must reconstruct everything from durable history.
type gitOutcomeInterruption struct {
	jobdb.WorkflowRuntime
	lease       jobdb.ExecutionLease
	match       string
	includeBase bool
	fired       bool
	cancel      context.CancelFunc
}

func (r *gitOutcomeInterruption) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	r.lease = l
	return l, err
}
func (r *gitOutcomeInterruption) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if err := r.WorkflowRuntime.PutChapter(ctx, req); err != nil {
		return err
	}
	if body, ok := req.Chapter.Body.(jobdb.ExecutionChapter); ok && !r.fired {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		if record.Kind == "result" && strings.Contains(record.ID, r.match) && (r.includeBase || !strings.Contains(record.ID, "workspace-base")) {
			r.fired = true
			if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}); err != nil {
				return err
			}
			r.cancel()
			return fmt.Errorf("injected loss after durable Git outcome")
		}
	}
	return nil
}

func runGitLifecycle(t *testing.T, rt jobdb.WorkflowRuntime, key jobdb.JobKey, recordedPolicy ...string) (jobworkflow.JobRunOutcome, error) {
	t.Helper()
	reg, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: engine}).Build()
	ws, err := NewRecipeWorker(deps, reg)
	require.NoError(t, err)
	ctx := celltest.Context(context.Background())
	if fault, ok := rt.(*gitOutcomeInterruption); ok {
		ctx, fault.cancel = context.WithCancel(ctx)
		defer fault.cancel()
	}
	guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
	guard.Issuer = permissions.JobDBIssuer{}
	var tasks []jobworkflow.TaskWorker
	for _, task := range ws.TaskWorkers {
		if len(recordedPolicy) != 0 && task.Name() == WorkspaceResolutionTaskType {
			task = workspacePolicyWorker{TaskWorker: task, policy: recordedPolicy[0]}
		}
		tasks = append(tasks, guard.WrapTaskWorker(task))
	}
	r, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "git-lifecycle", LeaseDuration: time.Minute})
	if err != nil {
		return jobworkflow.JobRunOutcome{}, err
	}
	return r.Run(nil)
}

func submitGitLifecycle(t *testing.T, rt jobdb.WorkflowRuntime, repo, yaml string) jobdb.JobKey {
	t.Helper()
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "source"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "git-tests", RecipeName: "git-lifecycle", JobContext: job, GitRef: "main", Cell: "test", CellRepo: celltest.Repo(job.GitBase.BaseRepo)}, jobdb.NewArtifactFromBytes("git-lifecycle"+starter.RecipeArtifactSuffix, []byte(yaml)))
	celltest.Create(t, rt, "git-tests", "test", celltest.Repo(repo))
	h, err := rt.SubmitJob(celltest.Context(context.Background()), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "git-tests", JobType: "recipe", Data: data, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}, Cell: "test", CellRepo: celltest.Repo(repo)}})
	require.NoError(t, err)
	return h.JobKey
}

func gitLifecycleOutput(t *testing.T, out jobworkflow.JobRunOutcome) map[string]any {
	t.Helper()
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
	raw, err := out.Output.GetData()
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	return result
}

func TestWorkspaceFloatingResumeAndRestart(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, mode := range []string{"const", "no-change", "retained-write", "exact-commit"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				initial := workspaceRepo(t, repo, "source")
				workspaceTestWorker(t)
				ref, plan := "main", "inputs: {}"
				if mode == "const" {
					plan = "const: true\n   inputs: {write: discarded}"
				}
				if mode == "retained-write" {
					plan = "inputs: {write: retained}"
				}
				if mode == "exact-commit" {
					ref = initial
				}
				yaml := fmt.Sprintf(`id: git-lifecycle
workspace: {cell: test, repo: %q, ref: %q}
sequence:
 - id: plan
   op: workspace_probe
   %s
 - id: implement
   op: workspace_probe
   inputs: {write: candidate}
 - id: inspect
   op: workspace_probe
   inputs: {read: candidate}
outputs:
 plan: '${{ sequence.plan.outputs.head }}'
 base: '${{ sequence.implement.outputs.head }}'
 candidate: '${{ sequence.inspect.outputs.head }}'
 text: '${{ sequence.inspect.outputs.text }}'
`, celltest.Repo(repo), ref, plan)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, yaml)
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: `scope:"op:plan"`}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				advance := func(name string) string {
					require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(name), 0600))
					require.NoError(t, runGit(repo, "git", "add", "."))
					require.NoError(t, runGit(repo, "git", "commit", "-m", name))
					hash, e := common.GetCommitHash(celltest.Context(context.Background()), repo, "HEAD")
					require.NoError(t, e)
					return hash
				}
				advanced := advance("upstream-b")
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				first := gitLifecycleOutput(t, out)
				require.Equal(t, initial, first["plan"])
				if mode == "const" || mode == "no-change" {
					require.Equal(t, advanced, first["base"])
				}
				if mode == "exact-commit" {
					require.Equal(t, initial, first["base"])
				}
				require.Equal(t, "test data", first["text"])
				history, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key})
				require.NoError(t, err)
				var scope string
				for _, entry := range history.Execution {
					if entry.Record.Kind == "scope" && strings.Contains(entry.Record.ID, `scope:"op:implement"`) {
						scope = entry.Record.ID
					}
				}
				require.NotEmpty(t, scope)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				submitter := jobdbschema.WorkflowEngine{Engine: engine, Registry: rt.WorkflowRuntime.(jobdb.JobSchemaRegistry)}
				restarted, err := starter.RestartRecipeJob(celltest.Context(context.Background()), submitter, key, scope, nil, starter.RestartOptions{Goal: "new", RequestID: "git-restart"})
				require.NoError(t, err)
				latest := advance("upstream-c")
				out, err = runGitLifecycle(t, rt, restarted.Key())
				require.NoError(t, err)
				second := gitLifecycleOutput(t, out)
				require.Equal(t, initial, second["plan"], "completed planning is replayed")
				if mode == "const" || mode == "no-change" {
					require.Equal(t, latest, second["base"], "discarded write must not pin restart")
				} else {
					require.Equal(t, first["base"], second["base"], "retained work and exact refs stay fixed")
				}
			})
		}
	}
}

type rebaseCaptureInput struct {
	Failure  map[string]any `json:"failure"`
	Evidence artifacts.Ref  `json:"evidence"`
}

func TestNativeRebaseFailureCatchAndResume(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			base := workspaceRepo(t, repo, "source")
			advance := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "rebase_test_advance"}, func(_ coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
				if err := os.WriteFile(filepath.Join(repo, "identity"), []byte("upstream change"), 0600); err != nil {
					return nil, err
				}
				if err := runGit(repo, "git", "add", "."); err != nil {
					return nil, err
				}
				if err := runGit(repo, "git", "commit", "-m", "upstream"); err != nil {
					return nil, err
				}
				hash, err := common.GetCommitHash(ctx, repo, "HEAD")
				return map[string]any{"hash": hash}, err
			})
			capture := coreops.NewActivityMappedOpV2[rebaseCaptureInput, map[string]any](coreops.OpMetadata{Type: "rebase_test_capture"}, func(d coreops.OpDependencies, ctx context.Context, input rebaseCaptureInput) (map[string]any, error) {
				key, ok := input.Evidence.StoredKey()
				if !ok {
					return nil, fmt.Errorf("missing durable evidence")
				}
				a, err := d.FindArtifact(key)
				if err != nil {
					return nil, err
				}
				r, err := a.Open()
				if err != nil {
					return nil, err
				}
				defer r.Close()
				data, err := io.ReadAll(r)
				if err != nil {
					return nil, err
				}
				head, err := common.GetCommitHash(ctx, d.WorktreePath(), "HEAD")
				return map[string]any{"failure": input.Failure, "head": head, "base": d.GitContext().ResolvedBaseHash, "evidence": string(data)}, err
			})
			workspaceTestWorker(t, thinpackrebase.GetRebaseOp(), advance, capture)
			yaml := `id: git-lifecycle
sequence:
 - id: write
   op: workspace_probe
   inputs: {write: identity}
 - id: advance
   op: rebase_test_advance
 - id: integration
   inputs: {target: '${{ sequence.advance.outputs.hash }}'}
   vars: {target: '${{ inputs.target }}'}
   state:
     initial: replay
     states:
       replay:
         sequence:
          - id: nested
            sequence:
             - id: rebase_candidate
               op: rebase
               inputs: {target_base_hash: '${{ vars.target }}'}
         catch:
          - when: failure.code == 'REBASE_CONFLICT' && failure.attrs.rebase.workspace_restored
            to: recover
       recover:
         op: rebase_test_capture
         inputs:
           failure: '${{ transition.failure }}'
           evidence: '${{ transition.failure.attrs.rebase.diagnostics["_c2j/rebase/conflict-index.z"] }}'
   outputs: {captured: '${{ states.recover.outputs }}'}
outputs: {captured: '${{ sequence.integration.outputs.captured }}'}
`
			rt := newTimeoutReproRuntime(t, backend)
			key := submitGitLifecycle(t, rt, repo, yaml)
			fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: `task:"rebase:rebase"`}
			_, err := runGitLifecycle(t, fault, key)
			require.Error(t, err)
			require.True(t, fault.fired)
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			captured := gitLifecycleOutput(t, out)["captured"].(map[string]any)
			failure := captured["failure"].(map[string]any)
			require.Equal(t, "task_error", failure["kind"])
			require.Equal(t, thinpackrebase.ConflictCode, failure["code"])
			require.Equal(t, "rebase_candidate", failure["node"].(map[string]any)["id"])
			details := failure["attrs"].(map[string]any)["rebase"].(map[string]any)
			require.Equal(t, base, details["base_hash"])
			require.Equal(t, base, captured["base"])
			require.Equal(t, details["candidate_hash"], captured["head"])
			require.Contains(t, captured["evidence"], "identity")
			require.Len(t, details["conflicts"], 1)
			// Completed replay needs neither Git nor the failed worker's directory.
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
			require.NoError(t, err)
			require.NoError(t, os.Rename(repo, repo+"-offline"))
			replayed, err := engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			raw, err := replayed.GetData()
			require.NoError(t, err)
			original, err := out.Output.GetData()
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(raw))
		})
	}
}
