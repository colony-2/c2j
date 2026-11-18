package compiler

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Keep real child resolution and task execution, but dispatch receivers only
// after the parent has committed its contributions and suspended on its wait.
func (f *groupingFixture) installChildren(t *testing.T, children map[string]string) {
	t.Helper()
	repo := f.start.JobContext.GitBase.BaseRepo
	for name, yaml := range children {
		path := filepath.Join(repo, ".c2", "recipes", name+".yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(yaml), 0644))
	}
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "Install grouped receivers"))
	deps := ops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: f.eng}).Build()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	f.workers, err = NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{
		RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{}),
		WrapTaskWorker:     f.guard.(*executionruntime.Runtime).WrapTaskWorker,
	})
	require.NoError(t, err)
}

func (f *groupingFixture) startParent(t *testing.T) jobref.Ref {
	t.Helper()
	start := f.start
	start.RecipeName = "parent"
	ref, err := starter.StartRecipeJob(f.ctx, start, f.eng, f.rec)
	require.NoError(t, err)
	return ref
}

func TestGroupingDurableMixedChildBindings(t *testing.T) {
	withRegisteredOps(t, recipeops.GetOps()...)
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, `id: parent
perms: {jobdb: {jobs: {submit: true}}}
sequence:
- id: children
  child_group:
    mode: run_and_get_result
    children:
    - key: a
      recipe: batch
      cell_name: test
      grouping: {key: shared, content: {id: a}}
    - key: b
      recipe: batch
      cell_name: test
      grouping: {key: shared, content: {id: b}}
    - key: ordinary
      recipe: ordinary
      cell_name: test
    - key: skip
      recipe: batch
      when: false
outputs: {children: '${{ sequence.children.outputs.children }}'}
`)
			f.installChildren(t, map[string]string{"batch": groupingCountRecipe, "ordinary": "id: ordinary\nsequence: []\noutputs: {ordinary: true}\n"})
			parent := f.startParent(t)
			require.Equal(t, wf.JobRunSuspended, f.run(t, parent.Key(), nil).Status)
			jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 3, "parent, shared batch and ordinary child; skipped child submits nothing")
			var batchID string
			for _, job := range jobs.Jobs {
				if job.JobKey.JobId == parent.JobID {
					continue
				}
				var meta map[string]any
				require.NoError(t, json.Unmarshal(job.Metadata, &meta))
				if job.AppendKey == "shared" {
					batchID = job.JobKey.JobId
					require.NotContains(t, meta, "parent_job_id")
				} else {
					require.Equal(t, parent.JobID, meta["parent_job_id"])
				}
				require.Equal(t, wf.JobRunCompleted, f.run(t, job.JobKey, nil).Status)
			}
			require.NotEmpty(t, batchID)
			parentRun := f.run(t, parent.Key(), nil)
			for i := 0; parentRun.Status == wf.JobRunSuspended && i < 8; i++ {
				parentRun = f.run(t, parent.Key(), nil)
			}
			require.Equal(t, wf.JobRunCompleted, parentRun.Status, "%v", parentRun.JobError)
			var out map[string]any
			require.NoError(t, json.Unmarshal(parentRun.Output.GetDataOrPanic(), &out))
			var children []recipeops.ChildGroupChildRecord
			raw, err := json.Marshal(out["children"])
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &children))
			require.Len(t, children, 4)
			for i, key := range []string{"a", "b"} {
				require.Equal(t, key, children[i].Key)
				require.Equal(t, batchID, children[i].JobID)
				require.Equal(t, batchID, children[i].Result.Job)
				require.Equal(t, "completed", children[i].Status)
				require.EqualValues(t, 2, children[i].Outputs["count"])
				require.Equal(t, []any{"a", "b"}, children[i].Outputs["ids"])
			}
			require.Equal(t, children[0].GoalID, children[1].GoalID)
			require.Equal(t, children[0].CellID, children[1].CellID)
			require.Equal(t, "completed", children[2].Status)
			require.Equal(t, true, children[2].Outputs["ordinary"])
			require.Equal(t, "skipped", children[3].Status)
			replayed, err := f.eng.ReplayJobRun(f.ctx, wf.ReplayRunRequest{JobKey: parent.Key(), JobWorker: f.workers.JobWorker})
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(mustGroupingOutput(t, replayed)["children"]))
			jobs, err = f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, AppendKeys: []string{"shared"}})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 1, "replay must not append to a successor")
		})
	}
}

func mustGroupingOutput(t *testing.T, data jobdb.TaskData) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data.GetDataOrPanic(), &out))
	return out
}

func TestGroupingDurablePartialFanoutReportsAcceptedHandles(t *testing.T) {
	withRegisteredOps(t, recipeops.GetOps()...)
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, `id: parent
perms: {jobdb: {jobs: {submit: true, cellNames: [test]}}}
sequence:
- id: submit
  op: recipes.run
  retry: {maximum_attempts: 2, initial_interval: 1ns}
  inputs:
    git_ref: main
    recipes:
    - name: batch
      cell_name: test
      grouping: {key: partial, content: {id: accepted}}
    - name: batch
      cell_name: other
      grouping: {key: denied, content: {id: denied}}
outputs: {}
`)
			f.installChildren(t, map[string]string{"batch": groupingCountRecipe})
			otherRepo := filepath.Join(t.TempDir(), "other")
			workspaceRepo(t, otherRepo, "other")
			celltest.Create(t, f.rt, "tenant", "other", celltest.Repo(otherRepo))
			parent := f.startParent(t)
			out := f.run(t, parent.Key(), nil)
			// Retry scheduling can yield the lease, but no receiver is dispatched
			// until both actual operation attempts have ended.
			for deadline := time.Now().Add(10 * time.Second); out.Status == wf.JobRunSuspended && time.Now().Before(deadline); {
				out = f.run(t, parent.Key(), nil)
			}
			require.Equal(t, wf.JobRunFailed, out.Status)
			require.ErrorContains(t, out.JobError, "permission")
			jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 2, "only parent and accepted batch; no denied or ordinary fallback")
			var batch jobdb.JobKey
			for _, job := range jobs.Jobs {
				if job.AppendKey == "partial" {
					batch = job.JobKey
				}
			}
			require.NotEmpty(t, batch.JobId)
			run, err := jobdb.GetJobRun(f.ctx, f.rt, jobdb.GetJobRunRequest{JobKey: parent.Key(), IncludeOutputs: true})
			require.NoError(t, err)
			reports := 0
			tasks, err := swfutil.ExecutionTasks(run)
			require.NoError(t, err)
			for _, task := range tasks {
				if !strings.HasPrefix(task.TaskType, "recipes.run:") {
					continue
				}
				for _, ta := range task.Attempts {
					if ta.Output == nil {
						continue
					}
					decoded, err := decodeTaskOutput(ta.Output.Data)
					require.NoError(t, err)
					if len(decoded.Activity.Jobs.Items) == 0 {
						continue
					}
					require.Len(t, decoded.Activity.Jobs.Items, 1)
					require.Equal(t, batch.JobId, decoded.Activity.Jobs.Items[0].JobID)
					require.Empty(t, decoded.Activity.Jobs.Items[0].ParentInvocationHash)
					reports++
				}
			}
			require.Equal(t, 2, reports, "both failed task outputs retain the observed shared receipt")
			result := f.run(t, batch, nil)
			require.Equal(t, wf.JobRunCompleted, result.Status, "%v", result.JobError)
			var got map[string]any
			require.NoError(t, json.Unmarshal(result.Output.GetDataOrPanic(), &got))
			require.EqualValues(t, 2, got["count"], "accepted but uncheckpointed fan-out legitimately duplicates on retry")
			require.Equal(t, []any{"accepted", "accepted"}, got["ids"])
		})
	}
}

func TestGroupingWorkspaceAndOpaqueSecretForwarding(t *testing.T) {
	workspaceTestWorker(t)
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, `id: parent
perms: {jobdb: {jobs: {submit: true, cellNames: [other]}, secrets: {resolve: [allowed]}}}
sequence:
- id: branch
  workspace: {cell: other}
  sequence:
  - id: edit
    op: workspace_probe
    inputs: {write: candidate}
  - id: submit
    op: recipes.run
    inputs:
      recipes:
      - name: batch
        cell_name: '${{ context.workspace.cell }}'
        inputs: {token: '${{ context.secrets.allowed }}'}
        grouping: {key: scoped, content: {cell: '${{ context.workspace.cell }}'}}
  outputs: {items: '${{ sequence.submit.outputs.items }}'}
outputs: {items: '${{ sequence.branch.outputs.items }}'}
`)
			f.installChildren(t, map[string]string{"batch": `id: batch
perms: {jobdb: {jobs: {submit: false}, secrets: {resolve: []}}}
input_schema:
  token: {type: string, secret: true, required: true}
inputs: {token: '${{ inputs.token }}'}
sequence:
- id: read
  op: workspace_probe
  inputs: {read: candidate}
outputs:
  text: '${{ sequence.read.outputs.text }}'
  cell: '${{ sequence.read.outputs.cell }}'
  owner: '${{ sequence.read.outputs.owner }}'
  scope: '${{ sequence.read.outputs.scope }}'
  selected: '${{ context.grouping.appends[0].content.cell }}'
`})
			otherRepo := filepath.Join(t.TempDir(), "other")
			workspaceRepo(t, otherRepo, "other")
			celltest.Create(t, f.rt, "tenant", "other", celltest.Repo(otherRepo))
			parent := f.startParent(t)
			parentRun := f.run(t, parent.Key(), nil)
			require.Equal(t, wf.JobRunCompleted, parentRun.Status, "%v", parentRun.JobError)
			var output struct {
				Items []recipeops.StartedJob `json:"items"`
			}
			require.NoError(t, json.Unmarshal(parentRun.Output.GetDataOrPanic(), &output))
			require.Len(t, output.Items, 1)
			child := output.Items[0]
			require.Equal(t, "other", child.Cell)
			require.NotEmpty(t, child.CellID)
			key := jobdb.JobKey{TenantId: "tenant", JobId: child.JobID}
			chapters, err := f.rt.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			var captured workflowctl.StartJob
			require.NoError(t, json.Unmarshal(chapters[0].Body.(jobdb.JobStartChapter).Input.Data, &captured))
			raw, err := json.Marshal(captured.Inputs["token"])
			require.NoError(t, err)
			var reference secrets.Reference
			require.NoError(t, json.Unmarshal(raw, &reference))
			require.Equal(t, "allowed", reference.Name, "forward the opaque reference without requesting plaintext")
			require.Nil(t, captured.Parent)
			require.NotNil(t, captured.JobContext.Workspace)
			require.NotEmpty(t, captured.JobContext.RestoreArtifact)
			require.NotEmpty(t, captured.JobContext.Workspace.ParentScopeID)
			childRun := f.run(t, key, nil)
			require.Equal(t, wf.JobRunCompleted, childRun.Status, "%v", childRun.JobError)
			var got map[string]any
			require.NoError(t, json.Unmarshal(childRun.Output.GetDataOrPanic(), &got))
			require.Equal(t, "other data", got["text"], "child inherits the selected workspace snapshot")
			require.Equal(t, "other", got["owner"])
			require.Equal(t, "other", got["cell"])
			require.Equal(t, "other", got["selected"])
			require.Equal(t, captured.JobContext.Workspace.ScopeID, got["scope"])
		})
	}
}
