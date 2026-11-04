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
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestMixedSchemaIncludesRetainTheirOwnContracts(t *testing.T) {
	for _, parentVersion := range []string{"", "schema_version: 2\n"} {
		t.Run(strings.TrimSpace(parentVersion), func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "root.yaml"), []byte("sequence: []\n"), 0600))
			ref := objects.ValidationRef("test.session/v1")
			legacy := `id: legacy
input_schema:
  session: {type: object, object_type: test.session/v1, required: true}
inputs: {session: '${{ inputs.session }}'}
sequence: []
outputs: {session: '${{ inputs.session }}'}
`
			modern := `schema_version: 2
id: modern
input_schema:
  data:
    type: object
    properties:
      title: {type: string, default_value: Design}
  session: {type: checkpoint, required: true}
inputs:
  data: '${{ inputs.data }}'
  session: '${{ inputs.session }}'
sequence:
  - id: legacy
    include: ./legacy.yaml
    perms: {limit: {jobdb: {}}}
    inputs: {session: '${{ inputs.session }}'}
outputs:
  data: '${{ inputs.data }}'
  session: '${{ sequence.legacy.outputs.session }}'
`
			require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.yaml"), []byte(legacy), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "modern.yaml"), []byte(modern), 0600))
			sessionType := "object"
			if parentVersion != "" {
				sessionType = "checkpoint"
			}
			root, err := recipe.LoadRecipeFromString([]byte(parentVersion + fmt.Sprintf(`id: parent
input_schema:
  session: {type: %s, required: true}
inputs: {session: '${{ inputs.session }}'}
sequence:
  - id: modern
    include: ./modern.yaml
    perms: {limit: {jobdb: {}}}
    inputs:
      data: {}
      session: '${{ inputs.session }}'
outputs:
  data: '${{ sequence.modern.outputs.data }}'
  session: '${{ sequence.modern.outputs.session }}'
`, sessionType)))
			require.NoError(t, err)
			expanded, err := ResolveInlineRecipes(celltest.Context(context.Background()), *root, InlineResolutionOptions{RootFile: filepath.Join(dir, "root.yaml")})
			require.NoError(t, err)
			raw, err := marshalRecipeYAML(expanded.Recipe)
			require.NoError(t, err)
			require.Contains(t, string(raw), "composite_schema_version: 2")
			require.NoError(t, os.Remove(filepath.Join(dir, "legacy.yaml")))
			require.NoError(t, os.Remove(filepath.Join(dir, "modern.yaml")))
			persisted, err := recipe.LoadInternalRecipeFromString(raw)
			require.NoError(t, err)
			job, git := GenerateTestContext()
			counter := &countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "mixed"}}
			out, _, err := ExecuteRecipe(newWorkflowContext(counter), *persisted, map[string]any{"session": ref}, job, git)
			require.NoError(t, err)
			require.Equal(t, map[string]any{"title": "Design"}, out["data"])
			want, _, err := objects.Parse(ref)
			require.NoError(t, err)
			got, ok, err := objects.Parse(out["session"])
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, want, got)
			require.Zero(t, counter.calls)
		})
	}
}

func TestSchemaVersionDurableReplay(t *testing.T) {
	for _, version := range []string{"", "schema_version: 1\n", "schema_version: 2\n"} {
		for _, inlineHistory := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/inline=%t", strings.TrimSpace(version), inlineHistory), func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				rt := newTimeoutReproRuntime(t, "sqlite")
				root := version + `id: root
input_schema:
  title: {type: string, default_value: Design}
inputs: {title: '${{ inputs.title }}'}
sequence:
  - id: included
    include: child
    perms: {limit: {jobdb: {}}}
    inputs: {title: '${{ inputs.title }}'}
outputs: {title: '${{ sequence.included.outputs.title }}'}
`
				child := version + `id: child
input_schema:
  title: {type: string, required: true}
  session: {type: object}
inputs: {title: '${{ inputs.title }}'}
sequence: []
outputs: {title: '${{ inputs.title }}'}
`
				if strings.Contains(version, "2") {
					child = strings.Replace(child, "session: {type: object}", "session: {type: checkpoint}\n  annotations:\n    type: array\n    default_value: [{path: design.md}]\n    items:\n      type: object\n      properties:\n        path: {type: string, required: true}\n        title: {type: string, default_value: Design}", 1)
					child = strings.Replace(child, "inputs: {title: '${{ inputs.title }}'}", "inputs: {title: '${{ inputs.annotations[0].title }}'}", 1)
				}
				available := true
				loads := 0
				resolver := &conventionResolver{
					resolve: func(selector string) (RecipeSourceResolution, error) {
						if !available {
							return RecipeSourceResolution{}, fmt.Errorf("source is gone")
						}
						return RecipeSourceResolution{SourceKind: RecipeSourceKindServerRef, SubmittedSelector: selector, ResolvedSelector: selector + "@pinned"}, nil
					},
					load: func(r RecipeSourceResolution) ([]byte, error) {
						loads++
						if !available {
							return nil, fmt.Errorf("source is gone")
						}
						if r.SubmittedSelector == "child" {
							return []byte(child), nil
						}
						return []byte(root), nil
					},
				}
				data := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{TenantId: "schema-replay", RecipeName: "root", Cell: "test", CellRepo: celltest.Repo("")})
				celltest.Create(t, rt, "schema-replay", "test", "example.test/test")
				handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "schema-replay", JobType: "recipe", Data: data, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}, Cell: "test", CellRepo: "example.test/test"}})
				require.NoError(t, err)
				worker := NewRecipeJobWorker(RecipeJobWorkerOptions{RootSourceResolver: resolver})
				sourceTask := newRootSourceResolutionTaskWorker(resolver)
				if inlineHistory {
					sourceTask = inlineRootSourceWorker{sourceTask}
				}
				firstCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				interrupted := &rootSourceInterruption{WorkflowRuntime: rt, cancel: cancel}
				first, err := jobworkflow.GetJobForRun(firstCtx, interrupted, jobworkflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, TaskWorkers: []jobworkflow.TaskWorker{NewCellContextTaskWorker(), sourceTask}, WorkerID: "first", LeaseDuration: time.Minute})
				require.NoError(t, err)
				_, err = first.Run(nil)
				require.Error(t, err)
				require.True(t, interrupted.fired)
				available = false
				out := probeRun(t, rt, handle.JobKey, NewRecipeJobWorker(RecipeJobWorkerOptions{RootSourceResolver: resolver}), sourceTask)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, 2, loads)
				before, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				require.NoError(t, probeReplay(t, rt, handle.JobKey, NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true, RootSourceResolver: resolver})))
				after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				require.Equal(t, before, after)
				require.Equal(t, 2, loads)
				// Assert the output, not merely that the workflow completed.
				run, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: handle.JobKey})
				require.NoError(t, err)
				encoded, err := json.Marshal(run)
				require.NoError(t, err)
				require.Contains(t, string(encoded), `"title":"Design"`)
			})
		}
	}
}

func TestStructuredInputsThroughChildJobAndWorkerReplacement(t *testing.T) {
	ctx := celltest.Context(context.Background())
	workspaceTestWorker(t)
	repo := t.TempDir()
	workspaceRepo(t, repo, "root")
	child := `schema_version: 2
id: child
input_schema:
  annotations:
    type: array
    items:
      type: object
      properties:
        path: {type: string, required: true}
        title: {type: string, default_value: Design}
  labels: {type: object, additionalProperties: {type: string}}
inputs:
  annotations: '${{ inputs.annotations }}'
  labels: '${{ inputs.labels }}'
sequence: []
outputs:
  annotations: '${{ inputs.annotations }}'
  labels: '${{ inputs.labels }}'
`
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".c2/recipes"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".c2/recipes/child.yaml"), []byte(child), 0644))
	require.NoError(t, runGit(repo, "git", "add", "."))
	require.NoError(t, runGit(repo, "git", "commit", "-m", "structured child"))
	parent, err := recipe.LoadRecipeFromString([]byte(`schema_version: 2
id: parent
input_schema:
  annotations: {type: array, items: {type: object}}
  labels: {type: object, additionalProperties: {type: string}}
inputs:
  annotations: '${{ inputs.annotations }}'
  labels: '${{ inputs.labels }}'
sequence:
  - id: children
    child_group:
      mode: run_and_get_result
      children:
        - key: child
          recipe: child
          inputs:
            annotations: '${{ inputs.annotations }}'
            labels: '${{ inputs.labels }}'
outputs:
  result: '${{ sequence.children.outputs.children[0].outputs }}'
`))
	require.NoError(t, err)
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	eng, stop := objectEngineAt(t, dbPath)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
	celltest.Create(t, eng, "object-tests", "test", celltest.Repo(job.GitBase.BaseRepo))
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "object-tests", RecipeName: "parent", JobContext: job, GitRef: "main", Inputs: map[string]any{
		"annotations": []any{map[string]any{"path": "design.md"}},
		"labels":      map[string]any{"component": "api"},
	}, Cell: "test", CellRepo: celltest.Repo(job.GitBase.BaseRepo),
	}, eng, *parent)
	require.NoError(t, err)
	key := ref.Key()
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 15*time.Second, key, eng))
	stop()
	eng, _ = objectEngineAt(t, dbPath)
	result, err := swfutil.JobResult(ctx, eng, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	require.JSONEq(t, `{"result":{"annotations":[{"path":"design.md","title":"Design"}],"labels":{"component":"api"}}}`, string(raw))
	_, err = eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key})
	require.NoError(t, err)
}

func TestEmptyIncludeSchemaVersions(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3} {
		meta := recipe.NodeMetadata{Internal: &recipe.NodeInternalMetadata{
			Inline: &recipe.InlineInclusionMetadata{}, CompositeSchemaVersion: version,
		}}
		got, err := prepareCompositeInputs(meta, map[string]any{"extra": "value"})
		if version < 2 {
			require.NoError(t, err)
			require.Equal(t, map[string]any{"extra": "value"}, got)
		} else if version == 2 {
			require.ErrorContains(t, err, "extra")
		} else {
			require.ErrorContains(t, err, "schema_version")
		}
	}
}
