package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type hookArtifactCheckWorker struct {
	jobworkflow.TaskWorker
	t      *testing.T
	legacy bool
	loads  *atomic.Int32
}

func (w hookArtifactCheckWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}

func (w hookArtifactCheckWorker) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := w.TaskWorker.(interface {
		RunTask(context.Context, jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error)
	}).RunTask(ctx, tc, in)
	if err != nil {
		return nil, err
	}
	decoded, err := decodeTaskOutput(out.GetDataOrPanic())
	require.NoError(w.t, err)
	raw, err := json.Marshal(decoded.Activity.OpOutput)
	require.NoError(w.t, err)
	var result hookResolution
	require.NoError(w.t, json.Unmarshal(raw, &result))
	if result.RecipeYAMLArtifact == "" {
		return out, nil // Pin phase has no recipe content.
	}
	w.loads.Add(1)
	require.Empty(w.t, result.RecipeYAML)
	art, err := findResolvedRecipeYAMLArtifact(out, result.RecipeYAMLArtifact)
	require.NoError(w.t, err)
	require.NotNil(w.t, art)
	yaml, err := art.Bytes(tc.Context())
	require.NoError(w.t, err)
	if !w.legacy {
		require.Greater(w.t, len(yaml), 1_240_000)
	}
	artifacts, err := out.GetArtifacts()
	require.NoError(w.t, err)
	require.Len(w.t, artifacts, 2, "source artifact must preserve the candidate's thin pack")
	if !w.legacy {
		return out, nil
	}
	// Simulate a recorded hook-load result from the previous writer. Keep the
	// thin pack while replacing only the source representation.
	delete(decoded.Activity.OpOutput, "recipe_yaml_artifact")
	decoded.Activity.OpOutput["recipe_yaml"] = string(yaml)
	env, err := coretask.NewOutputEnvelope(decoded.Kind, decoded.Activity)
	if err != nil {
		return nil, err
	}
	var retained []jobdb.Artifact
	for _, item := range artifacts {
		if item.Name() != result.RecipeYAMLArtifact {
			retained = append(retained, item)
		}
	}
	return jobdb.NewTaskData(env, retained...)
}

func TestHookRecipeArtifactAndLegacyField(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			size := 1_300_000
			if legacy {
				size = 1000
			}
			commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: check\ndesc: "+strings.Repeat("x", size)+"\nsequence: []\noutputs: {ok: true}\n")
			ws := workspaceTestWorker(t)
			var loads atomic.Int32
			for i, worker := range ws.TaskWorkers {
				if worker.Name() == hookResolveTaskType {
					ws.TaskWorkers[i] = hookArtifactCheckWorker{worker, t, legacy, &loads}
				}
			}
			out, _ := runWorkspaceRecipe(t, ws, `id: workspace-test
sequence:
 - id: edit
   op: workspace_probe
   inputs: {write: candidate.txt}
 - id: check
   include: cell://.c2/hooks/check.yaml
outputs: {ok: "{{ sequence.check.outputs.ok }}"}
`, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}})
			require.Equal(t, true, out["ok"])
			require.EqualValues(t, 1, loads.Load())
		})
	}
}
