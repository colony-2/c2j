package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRecipeSubmissionInputsVisibility(t *testing.T) {
	for declared := 0; declared < 4; declared++ {
		for _, mode := range []ExecutionMode{ExecutionModeRun, ExecutionModeValidate} {
			t.Run(fmt.Sprintf("declared=%d/mode=%s", declared, mode), func(t *testing.T) {
				schema := "input_schema:\n  value: {type: string, required: true}\n"
				want := map[string]any{"value": "run"}
				direct := ""
				for i, key := range []string{"title", "prompt"} {
					if declared&(1<<i) != 0 {
						schema += "  " + key + ": {type: string}\n"
						want[key] = key
						direct += "  " + key + ": '${{ inputs." + key + " }}'\n"
					}
				}
				rec, err := recipe.LoadRecipeFromString([]byte("id: visibility\n" + schema + "inputs:\n  all: '${{ inputs }}'\n" + direct + "sequence: []\noutputs:\n  all: '${{ inputs.all }}'\n" + direct))
				require.NoError(t, err)
				job, git := GenerateTestContext()
				counter := &countingJobContext{jobKey: jobdb.JobKey{TenantId: "test", JobId: "visibility"}}
				out, _, err := ExecuteRecipe(newWorkflowContext(counter), *rec, map[string]any{"value": "run", "title": "title", "prompt": "prompt"}, job, git, ExecutionOptions{Mode: mode})
				require.NoError(t, err)
				require.Equal(t, want, out["all"])
				for key, value := range want {
					if key != "value" {
						require.Equal(t, value, out[key])
					}
				}
				require.Zero(t, counter.calls)
			})
		}
	}
}

func TestRecipeSubmissionInputsValidationDefaults(t *testing.T) {
	meta := recipe.RecipeMetadata{InputSchema: map[string]recipe.InputSchema{"value": {Type: "string", Required: true}, "title": {Type: "string", Default: "Default"}}}
	got, err := prepareRecipeInputs(meta, nil, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Equal(t, "Default", got["title"])
	require.Contains(t, got, "value")
	require.NotContains(t, got, "prompt")
	_, err = prepareRecipeInputs(meta, map[string]any{}, ExecutionOptions{Mode: ExecutionModeValidate})
	require.Error(t, err, "actual empty inputs must retain required validation")
}

func TestRecipeSubmissionInputsProfilesAndMissingReads(t *testing.T) {
	for _, mode := range []ExecutionMode{ExecutionModeRun, ExecutionModeValidate} {
		for _, expression := range []string{
			`${{ (has(inputs.title) || has(inputs.prompt)) ? "../invalid" : "safe" }}`,
			"${{ inputs.title }}", "${{ inputs.prompt }}",
		} {
			t.Run(string(mode)+"/"+expression, func(t *testing.T) {
				rec, err := recipe.LoadRecipeFromString([]byte("id: profile\ngit_profile: '" + expression + "'\nsequence: []\noutputs: {ok: true}\n"))
				require.NoError(t, err)
				job, git := GenerateTestContext()
				counter := &countingJobContext{jobKey: jobdb.JobKey{TenantId: "test", JobId: "profile"}}
				out, _, err := ExecuteRecipe(newWorkflowContext(counter), *rec, map[string]any{"title": "label", "prompt": "text"}, job, git, ExecutionOptions{Mode: mode})
				if strings.Contains(expression, "has(") {
					require.NoError(t, err)
					require.Equal(t, true, out["ok"])
				} else {
					require.Error(t, err)
				}
				require.Zero(t, counter.calls)
			})
		}
		for _, key := range []string{"title", "prompt"} {
			rec, err := recipe.LoadRecipeFromString([]byte("id: missing\ninputs:\n  value: '${{ inputs." + key + " }}'\nsequence: []\noutputs: {value: '${{ inputs.value }}'}\n"))
			require.NoError(t, err)
			job, git := GenerateTestContext()
			counter := &countingJobContext{jobKey: jobdb.JobKey{TenantId: "test", JobId: "missing"}}
			_, _, err = ExecuteRecipe(newWorkflowContext(counter), *rec, map[string]any{"title": "label", "prompt": "text"}, job, git, ExecutionOptions{Mode: mode})
			require.Error(t, err)
		}
	}
}

func TestRecipeSubmissionInputsChildJobs(t *testing.T) {
	ctx := celltest.Context(context.Background())
	workspaceTestWorker(t)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2/recipes"), 0755))
	parent := "id: parent\nsequence:\n  - id: children\n    child_group:\n      mode: run_and_get_result\n      children:\n"
	wants := make(map[string]map[string]any)
	for declared := 0; declared < 4; declared++ {
		name := fmt.Sprintf("child-%d", declared)
		schema := "input_schema:\n  value: {type: string}\n"
		want := map[string]any{"value": "run"}
		for i, key := range []string{"title", "prompt"} {
			if declared&(1<<i) != 0 {
				schema += "  " + key + ": {type: string}\n"
				want[key] = key
			}
		}
		wants[name] = want
		child := "schema_version: 2\nid: " + name + "\n" + schema + "inputs: {all: '${{ inputs }}'}\nsequence: []\noutputs: {all: '${{ inputs.all }}'}\n"
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2/recipes", name+".yaml"), []byte(child), 0644))
		parent += "        - key: " + name + "\n          recipe: " + name + "\n          inputs: {value: run, title: title, prompt: prompt}\n"
	}
	parent += "outputs:\n  children: '${{ sequence.children.outputs.children }}'\n"
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "submission input children"))
	rec, err := recipe.LoadRecipeFromString([]byte(parent))
	require.NoError(t, err)
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	engine, stop := objectEngineAt(t, dbPath)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	celltest.Create(t, engine, "object-tests", "test", celltest.Repo(repo))
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "parent", JobContext: job, GitRef: "main", Cell: "test", CellRepo: celltest.Repo(repo)}, engine, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 15*time.Second, ref.Key(), engine))
	result, err := swfutil.JobResult(ctx, engine, ref.Key())
	require.NoError(t, err)
	var output map[string]any
	require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &output))
	children, ok := output["children"].([]any)
	require.True(t, ok)
	require.Len(t, children, 4)
	for _, raw := range children {
		child := raw.(map[string]any)
		name := child["key"].(string)
		require.Equal(t, wants[name], child["outputs"].(map[string]any)["all"])
	}
	stop()
	rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: dbPath})
	require.NoError(t, err)
	defer rt.Close(ctx)
	listed, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"object-tests"}})
	require.NoError(t, err)
	childCount := 0
	for _, child := range listed.Jobs {
		if child.JobKey.JobId == ref.JobID {
			continue
		}
		childCount++
		var metadata starter.JobMetadata
		require.NoError(t, json.Unmarshal(child.Metadata, &metadata))
		require.Equal(t, "title", metadata.Title)
		require.Equal(t, "prompt", metadata.Prompt)
		chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: child.JobKey})
		require.NoError(t, err)
		first, ok := chapters[0].Body.(jobdb.JobStartChapter)
		require.True(t, ok)
		var start workflowctl.StartJob
		require.NoError(t, json.Unmarshal(first.Input.Data, &start))
		require.Equal(t, map[string]any{"value": "run", "title": "title", "prompt": "prompt"}, start.Inputs)
	}
	require.Equal(t, 4, childCount)
}
