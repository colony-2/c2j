package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type hookRestoreObservation struct {
	inputArtifacts int
	phase          string
	request        workerops.ActivityInvocationRequest
	result         hookResolution
	artifacts      []jobdb.ArtifactKey
}

// Observe the registered resolver, preserving the RunTask path used by the guard.
type observingHookRestoreWorker struct {
	jobworkflow.TaskWorker
	observe func(hookRestoreObservation)
}

func (w observingHookRestoreWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w observingHookRestoreWorker) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	var req workerops.ActivityInvocationRequest
	if err := json.Unmarshal(in.GetDataOrPanic(), &req); err != nil {
		return nil, err
	}
	out, err := w.TaskWorker.(interface {
		RunTask(context.Context, jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
	}).RunTask(ctx, tc, in)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeTaskOutput(out.GetDataOrPanic())
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(decoded.Activity.OpOutput)
	if err != nil {
		return nil, err
	}
	inputArts, err := in.GetArtifacts()
	if err != nil {
		return nil, err
	}
	observation := hookRestoreObservation{request: req, inputArtifacts: len(inputArts)}
	observation.phase, _ = req.Input["phase"].(string)
	if err := json.Unmarshal(raw, &observation.result); err != nil {
		return nil, err
	}
	arts, err := out.GetArtifacts()
	if err != nil {
		return nil, err
	}
	for _, a := range arts {
		key, err := a.ArtifactKey()
		if err == nil {
			observation.artifacts = append(observation.artifacts, key)
		}
	}
	w.observe(observation)
	return out, nil
}

func TestManagedSubmittedCandidateHookRestore(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprintf("const-prepared=%t", prepared), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "A")
			base := workspaceRepo(t, repo, "A")
			foreign := filepath.Join(t.TempDir(), "B")
			workspaceRepo(t, foreign, "B")
			require.NoError(t, os.WriteFile(filepath.Join(foreign, "foreign-only"), []byte("foreign-only"), 0644))
			commitHookFile(t, foreign, ".c2/hooks/check.yaml", "id: foreign\nsequence: []\noutputs: {marker: foreign-only}\n")
			preparation := ""
			prepOutput := ""
			if prepared {
				preparation = " - id: prepare\n   op: command_execution\n   const: true\n   inputs: {shell: sh, run: 'git rev-parse HEAD', timeout: 30s}\n"
				prepOutput = " prepared: '{{ sequence.prepare.outputs.stdout }}'\n"
			}
			childBody := `id: managed_hook_child
source_hooks: from_current
sequence:
` + preparation + ` - id: invoke
   include: cell://.c2/hooks/check.yaml
outputs:
 marker: '{{ sequence.invoke.outputs.marker }}'
 head: '{{ sequence.invoke.outputs.head }}'
` + prepOutput
			submit := coreops.NewActivityMappedOpV2[struct{}, workspaceSubmitProbeOutput](coreops.OpMetadata{Type: "submit_hook_candidate"}, func(deps coreops.OpDependencies, ctx context.Context, _ struct{}) (workspaceSubmitProbeOutput, error) {
				path := filepath.Join(deps.WorktreePath(), ".c2/hooks/check.yaml")
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					return workspaceSubmitProbeOutput{}, err
				}
				// This source and marker exist only in the submitted workspace, never base or foreign HEAD.
				body := "id: candidate_hook\nsequence:\n - id: read\n   const: true\n   op: workspace_probe\n   inputs: {read: candidate-only, absent: foreign-only}\noutputs: {marker: '{{ sequence.read.outputs.text }}', head: '{{ sequence.read.outputs.head }}'}\n"
				if err := os.WriteFile(path, []byte(body), 0644); err != nil {
					return workspaceSubmitProbeOutput{}, err
				}
				if err := os.WriteFile(filepath.Join(deps.WorktreePath(), "candidate-only"), []byte("candidate-only"), 0644); err != nil {
					return workspaceSubmitProbeOutput{}, err
				}
				child, err := recipe.LoadRecipeFromString([]byte(childBody))
				if err != nil {
					return workspaceSubmitProbeOutput{}, err
				}
				req, err := childbroker.NewSubmitRequest(ctx, workflowctl.StartJob{TenantId: deps.JobTool().GetJobKey().TenantId, RecipeName: "managed_hook_child", Cell: "test", CellRepo: celltest.Repo("")}, nil, *child)
				if err != nil {
					return workspaceSubmitProbeOutput{}, err
				}
				req.WorkspaceMode = "now"
				response, err := childbroker.SubmitLocal(ctx, req)
				return workspaceSubmitProbeOutput{JobID: response.JobID}, err
			})
			workspaceTestWorker(t, submit, commandop.GetOp())
			eng := newToyEngine(t, "workspace-tests", nil, map[string]string{"A": repo, "B": foreign})
			ctl := &workerworkflow.SWFWorkflowControl{Engine: eng}
			registry, err := workerops.NewActivityRegistry()
			require.NoError(t, err)
			var mu sync.Mutex
			var observations []hookRestoreObservation
			wrapped := false
			ws, err := NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build(), registry, RecipeJobWorkerOptions{WrapTaskWorker: func(w jobworkflow.TaskWorker) jobworkflow.TaskWorker {
				if w.Name() == hookResolveTaskType {
					wrapped = true
					resolver, ok := w.(hookResolveWorker)
					require.True(t, ok)
					require.Same(t, ctl, resolver.control)
					return observingHookRestoreWorker{TaskWorker: w, observe: func(o hookRestoreObservation) { mu.Lock(); defer mu.Unlock(); observations = append(observations, o) }}
				}
				return w
			}})
			require.NoError(t, err)
			require.True(t, wrapped)
			require.NoError(t, eng.RegisterWorkers(ws)) // native test engine applies executionruntime permission guard
			parent, err := recipe.LoadRecipeFromString([]byte(`id: parent
sequence:
 - id: foreign
   workspace: {cell: B}
   const: true
   op: workspace_probe
   inputs: {read: identity, absent: candidate-only}
 - id: submit
   op: submit_hook_candidate
 - id: await
   op: recipe.await_result_soft
   inputs: {target: {job: '{{ sequence.submit.outputs.job_id }}'}}
outputs:
 child: '{{ sequence.submit.outputs.job_id }}'
 result: '${{ sequence.await.outputs }}'
`))
			require.NoError(t, err)
			ctx := celltest.Context(context.Background())
			celltest.Create(t, eng, "workspace-tests", "A", celltest.Repo(repo))
			ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "workspace-tests", RecipeName: "parent", GitRef: "main", Cell: "A", CellRepo: celltest.Repo(repo), JobContext: contextual.JobContext{CellResolution: &cellref.Context{}, Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}}, eng, *parent)
			require.NoError(t, err)
			require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, time.Minute, ref.Key(), eng))
			data, err := swfutil.JobResult(ctx, eng, ref.Key())
			require.NoError(t, err)
			var out struct {
				Child  string `json:"child"`
				Result struct {
					Status  string         `json:"status"`
					Outputs map[string]any `json:"outputs"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal(data.GetDataOrPanic(), &out))
			require.Equal(t, "completed", out.Result.Status, string(data.GetDataOrPanic()))
			require.Equal(t, "candidate-only", out.Result.Outputs["marker"])
			candidate, ok := out.Result.Outputs["head"].(string)
			require.True(t, ok)
			require.NotEqual(t, base, candidate)
			if prepared {
				require.Equal(t, candidate, strings.TrimSpace(out.Result.Outputs["prepared"].(string)))
			}
			mu.Lock()
			captured := append([]hookRestoreObservation(nil), observations...)
			mu.Unlock()
			require.Len(t, captured, 2)
			require.Equal(t, "pin", captured[0].phase)
			require.Equal(t, "load", captured[1].phase)
			var original *jobdb.ArtifactKey
			for _, o := range captured {
				require.True(t, o.request.WorkspaceManaged)
				require.Zero(t, o.inputArtifacts, "initial submitted snapshot must exercise key-only restore routing")
				require.NotNil(t, o.request.RestoreArtifact)
				key := o.request.RestoreArtifact
				require.Equal(t, "__c2j_submit_workspace__", key.Name)
				require.Equal(t, out.Child, key.JobId)
				require.EqualValues(t, 0, key.TaskOrdinal)
				if original == nil {
					original = key
				}
				require.Equal(t, original, key)
				require.Equal(t, candidate, o.result.Candidate)
				require.Contains(t, o.artifacts, *key, "resolver must retain original snapshot identity")
				t.Logf("child=%s phase=%s base=%s candidate=%s restore=%+v", out.Child, o.phase, base, candidate, *key)
			}
			require.NotEmpty(t, captured[1].result.RecipeYAMLArtifact)
			_, err = os.Stat(filepath.Join(repo, "candidate-only"))
			require.True(t, os.IsNotExist(err))
			childKey := jobdb.JobKey{TenantId: "workspace-tests", JobId: out.Child}
			childResult, err := swfutil.JobResult(ctx, eng, childKey)
			require.NoError(t, err)
			require.NoError(t, os.RemoveAll(repo))
			require.NoError(t, os.RemoveAll(foreign))
			replayed, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: childKey, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			require.JSONEq(t, string(childResult.GetDataOrPanic()), string(replayed.GetDataOrPanic()))
			mu.Lock()
			afterReplay := len(observations)
			mu.Unlock()
			require.Len(t, captured, afterReplay, "durable replay must not rerun resolver tasks")
		})
	}
}
