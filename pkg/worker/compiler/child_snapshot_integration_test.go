package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestAwaitMergedChildPreservesParentSnapshotAndChildEvidence(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Snapshot Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "snapshot-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Snapshot Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "snapshot-test@example.com")
	for _, consult := range []bool{false, true} {
		for _, constant := range []bool{false, true} {
			t.Run(fmt.Sprintf("consult=%t/const=%t", consult, constant), func(t *testing.T) {
				root := t.TempDir()
				a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
				workspaceRepo(t, a, "A")
				seed := filepath.Join(root, "seed")
				bBase := workspaceRepo(t, seed, "B")
				require.NoError(t, runGit(root, "git", "clone", "--bare", seed, b))
				workspaceTestWorker(t, squashrebasemerge.GetOp())
				child := `id: child
version: "1"
sequence:
 - id: write
   op: workspace_probe
   inputs: {read: identity, write: child.txt, absent: experiment.txt}
 - id: merge
   op: squashrebasemerge
   inputs: {commit_message: "Child implementation"}
outputs:
 cell: "{{ sequence.write.outputs.text }}"
 merged_hash: "{{ sequence.merge.outputs.merged_hash }}"
`
				require.NoError(t, os.MkdirAll(filepath.Join(a, ".c2j", "recipes"), 0755))
				require.NoError(t, os.WriteFile(filepath.Join(a, ".c2j", "recipes", "child.yaml"), []byte(child), 0644))
				require.NoError(t, runGit(a, "git", "add", "."))
				require.NoError(t, runGit(a, "git", "commit", "-m", "Install child"))
				aBase, err := common.GetCommitHash(context.Background(), a, "HEAD")
				require.NoError(t, err)
				eng := newToyEngine(t, "snapshot-tests", nil)
				ctl := &workerworkflow.SWFWorkflowControl{Engine: eng}
				deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()
				registry, err := workerops.NewActivityRegistry()
				require.NoError(t, err)
				ws, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{})})
				require.NoError(t, err)
				require.NoError(t, eng.RegisterWorkers(ws))
				consultation := ""
				if consult {
					consultation = ` - id: consult
   workspace: {cell: B}
   op: workspace_probe
   inputs: {write: experiment.txt, absent: candidate.txt}
`
				}
				parent, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf(`id: parent
version: "1"
sequence:
 - id: candidate
   op: workspace_probe
   inputs: {write: candidate.txt}
%s - id: submit
   op: recipes.run
   inputs:
    git_ref: %q
    recipes:
     - name: child
       cell_name: B
       git: {base_repo: %q, base_ref: main, base_hash: %q}
 - id: await
   const: %t
   op: recipe.await_result_soft
   inputs: {job_id: "${{ sequence.submit.outputs.job_ids[0] }}"}
 - id: after
   op: workspace_probe
   inputs: {read: candidate.txt, absent: child.txt}
outputs:
 candidate: "{{ sequence.after.outputs.text }}"
 parent_head: "{{ sequence.after.outputs.head }}"
 child_id: "${{ sequence.submit.outputs.job_ids[0] }}"
 dependency: "${{ sequence.await.outputs }}"
`, consultation, bBase, b, bBase, constant)))
				require.NoError(t, err)
				job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}, CellResolution: &cellref.Context{Pattern: root + "/${{ cell }}"}}
				key, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "snapshot-tests", RecipeName: "parent", JobContext: job, GitRef: "main"}, eng, *parent)
				require.NoError(t, err)
				require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 20*time.Second, key, eng))
				result, err := swfutil.JobResult(context.Background(), eng, key)
				require.NoError(t, err)
				var out struct {
					Candidate  string `json:"candidate"`
					ParentHead string `json:"parent_head"`
					ChildID    string `json:"child_id"`
					Dependency struct {
						Status    string                         `json:"status"`
						Outputs   map[string]any                 `json:"outputs"`
						Artifacts map[string]recipeartifacts.Ref `json:"artifacts"`
					} `json:"dependency"`
				}
				raw, err := result.GetData()
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &out))
				require.Equal(t, "A data", out.Candidate)
				require.Equal(t, "completed", out.Dependency.Status)
				require.Equal(t, "B", out.Dependency.Outputs["cell"])
				merged, err := common.GetCommitHash(context.Background(), b, "main")
				require.NoError(t, err)
				require.NotEqual(t, bBase, merged)
				require.Equal(t, merged, out.Dependency.Outputs["merged_hash"])
				childPack, ok := out.Dependency.Artifacts[gitstate.ThinPackArtifactName].StoredKey()
				require.True(t, ok, "child snapshot remains available as dependency evidence")
				require.Equal(t, out.ChildID, childPack.JobId)
				artifact := ctl.GetArtifactLazy(context.Background(), key.TenantId, childPack)
				reader, err := artifact.Open()
				require.NoError(t, err, "child evidence must remain retrievable")
				require.NoError(t, reader.Close())
				arts, err := result.GetArtifacts()
				require.NoError(t, err)
				pack := findThinPack(arts)
				require.NotNil(t, pack)
				packKey, err := pack.ArtifactKey()
				require.NoError(t, err)
				require.Equal(t, key.JobId, packKey.JobId, "implicit snapshot belongs to the parent")
				restore := &gitstate.GitTaskContext{GlobalGitTaskContext: &gitstate.GlobalGitTaskContext{BaseRepo: a, BaseRef: "main", PersistHash: out.ParentHead, ParentHash: aBase}, WorktreePath: filepath.Join(t.TempDir(), "restore")}
				require.NoError(t, gitstate.NewController(nil).Restore(context.Background(), restore, pack))
				contents, err := os.ReadFile(filepath.Join(restore.WorktreePath, "candidate.txt"))
				require.NoError(t, err)
				require.Equal(t, "A data", string(contents))
				for _, name := range []string{"child.txt", "experiment.txt"} {
					_, err := os.Stat(filepath.Join(restore.WorktreePath, name))
					require.True(t, os.IsNotExist(err), name)
				}
			})
		}
	}
}
