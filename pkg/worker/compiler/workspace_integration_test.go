package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type workspaceProbeInput struct {
	Write   string `json:"write,omitempty"`
	Read    string `json:"read,omitempty"`
	Absent  string `json:"absent,omitempty"`
	Cell    string `json:"cell" default:"{{ context.workspace.cell }}"`
	Owner   string `json:"owner" default:"{{ context.workflow.cell }}"`
	Advance string `json:"advance,omitempty"`
	Fail    bool   `json:"fail,omitempty"`
}
type workspaceProbeOutput struct {
	Cell  string `json:"cell"`
	Owner string `json:"owner"`
	Scope string `json:"scope"`
	Text  string `json:"text"`
	Dir   string `json:"dir"`
	Head  string `json:"head"`
}

func workspaceRepo(t *testing.T, dir, cell string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
	for _, args := range [][]string{{"init", "-b", "main"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		require.NoError(t, runGit(dir, "git", args...))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "identity"), []byte(cell), 0644))
	require.NoError(t, runGit(dir, "git", "add", "."))
	require.NoError(t, runGit(dir, "git", "commit", "-m", "initial"))
	hash, err := common.GetCommitHash(context.Background(), dir, "HEAD")
	require.NoError(t, err)
	return hash
}
func workspaceTestWorker(t *testing.T) *jobworkflow.WorkSet {
	t.Helper()
	op := coreops.NewActivityMappedOpV2[workspaceProbeInput, workspaceProbeOutput](coreops.OpMetadata{Type: "workspace_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in workspaceProbeInput) (workspaceProbeOutput, error) {
		out := workspaceProbeOutput{Cell: in.Cell, Owner: in.Owner, Dir: deps.WorktreePath()}
		g := deps.GitContext()
		if g.Workspace != nil {
			out.Scope = g.Workspace.ScopeID
		}
		if in.Owner != g.CellName {
			return out, fmt.Errorf("owner changed: %s vs %s", in.Owner, g.CellName)
		}
		if g.Workspace != nil && in.Cell != g.Workspace.Cell {
			return out, fmt.Errorf("workspace cell mismatch: %s vs %s", in.Cell, g.Workspace.Cell)
		}
		if in.Absent != "" {
			if _, err := os.Stat(filepath.Join(out.Dir, in.Absent)); !os.IsNotExist(err) {
				return out, fmt.Errorf("unexpected file %s", in.Absent)
			}
		}
		if in.Write != "" {
			if err := os.WriteFile(filepath.Join(out.Dir, in.Write), []byte(in.Cell+" data"), 0644); err != nil {
				return out, err
			}
		}
		if in.Read != "" {
			b, err := os.ReadFile(filepath.Join(out.Dir, in.Read))
			if err != nil {
				return out, err
			}
			out.Text = string(b)
		}
		out.Head, _ = common.GetCommitHash(ctx, out.Dir, "HEAD")
		if in.Advance != "" {
			if err := os.WriteFile(filepath.Join(in.Advance, "moved"), []byte("new tip"), 0644); err != nil {
				return out, err
			}
			if err := runGit(in.Advance, "git", "add", "."); err != nil {
				return out, err
			}
			if err := runGit(in.Advance, "git", "commit", "-m", "advance"); err != nil {
				return out, err
			}
		}
		if in.Fail {
			return out, fmt.Errorf("intentional probe failure")
		}
		return out, nil
	})
	withRegisteredOps(t, append([]coreops.RegisterableOp{op}, recipeops.GetOps()...)...)
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
	require.NoError(t, err)
	return ws
}
func runWorkspaceRecipe(t *testing.T, ws *jobworkflow.WorkSet, yaml string, job contextual.JobContext) (map[string]any, []jobdb.Artifact) {
	t.Helper()
	rec, err := recipe.LoadRecipeFromString([]byte(yaml))
	require.NoError(t, err)
	eng := newToyEngineWithWorkSet(t, "workspace-tests", ws, nil)
	key, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "workspace-tests", RecipeName: "workspace-test", JobContext: job, GitRef: job.GitBase.BaseRef}, eng, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 10*time.Second, key, eng))
	result, err := swfutil.JobResult(context.Background(), eng, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	arts, err := result.GetArtifacts()
	require.NoError(t, err)
	return out, arts
}
func TestWorkspaceSnapshotsAndNestedScopes(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "A")
	b := filepath.Join(root, "B")
	c := filepath.Join(root, "C")
	hashes := map[string]string{a: workspaceRepo(t, a, "A"), b: workspaceRepo(t, b, "B"), c: workspaceRepo(t, c, "C")}
	ws := workspaceTestWorker(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}, CellResolution: &cellref.Context{Pattern: root + "/${{ cell }}", SelfRepo: a, SelfRef: "main"}}
	out, arts := runWorkspaceRecipe(t, ws, `id: workspace-test
version: "1"
sequence:
 - id: a
   op: workspace_probe
   inputs: {write: a.txt}
 - id: b
   workspace: {cell: B}
   vars: {selected: "{{ context.workspace.cell }}"}
   sequence:
    - id: write
      op: workspace_probe
      inputs: {write: b.txt, absent: a.txt}
    - id: c
      workspace: {cell: C}
      op: workspace_probe
      inputs: {write: c.txt, absent: b.txt}
    - id: read
      op: workspace_probe
      inputs: {read: b.txt, absent: c.txt}
   outputs:
     text: "{{ sequence.read.outputs.text }}"
     scope: "{{ sequence.read.outputs.scope }}"
     initial_scope: "{{ sequence.write.outputs.scope }}"
     selected: "{{ vars.selected }}"
 - id: resume
   op: workspace_probe
   inputs: {read: a.txt, absent: b.txt}
 - id: fresh
   workspace: {cell: "{{ sequence.b.outputs.selected }}"}
   op: workspace_probe
   inputs: {absent: b.txt, write: fresh.txt}
outputs:
 a: "{{ sequence.resume.outputs.text }}"
 a_head: "{{ sequence.resume.outputs.head }}"
 b: "{{ sequence.b.outputs.text }}"
 b_scope: "{{ sequence.b.outputs.scope }}"
 initial_scope: "{{ sequence.b.outputs.initial_scope }}"
 fresh_scope: "{{ sequence.fresh.outputs.scope }}"
 owner: "{{ sequence.fresh.outputs.owner }}"
`, job)
	require.Equal(t, "A data", out["a"])
	require.Equal(t, "B data", out["b"])
	require.Equal(t, "A", out["owner"])
	require.Equal(t, out["initial_scope"], out["b_scope"])
	require.NotEqual(t, out["b_scope"], out["fresh_scope"])
	for repo, hash := range hashes {
		head, err := common.GetCommitHash(context.Background(), repo, "HEAD")
		require.NoError(t, err)
		require.Equal(t, hash, head)
	}
	pack := findThinPack(arts)
	require.NotNil(t, pack)
	// The last op was B. The implicit job pack must still restore A's a.txt.
	restore := &gitstate.GitTaskContext{GlobalGitTaskContext: &gitstate.GlobalGitTaskContext{BaseRepo: a, BaseRef: "main", PersistHash: out["a_head"].(string), ParentHash: hashes[a]}, WorktreePath: filepath.Join(t.TempDir(), "a")}
	require.NoError(t, gitstate.NewController(nil).Restore(context.Background(), restore, pack))
	content, err := os.ReadFile(filepath.Join(restore.WorktreePath, "a.txt"))
	require.NoError(t, err)
	require.Equal(t, "A data", string(content))
	_, err = os.Stat(filepath.Join(restore.WorktreePath, "fresh.txt"))
	require.True(t, os.IsNotExist(err))
}
func TestWorkspaceConstPinsBaseAndRootVars(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "A")
	b := filepath.Join(root, "B")
	workspaceRepo(t, a, "A")
	initial := workspaceRepo(t, b, "B")
	ws := workspaceTestWorker(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}}
	yaml := fmt.Sprintf(`id: workspace-test
version: "1"
workspace: {cell: %q}
vars: {selected: "{{ context.workspace.cell }}"}
sequence:
 - id: first
   const: true
   op: workspace_probe
   inputs: {advance: %q, write: discard.txt}
 - id: next
   op: workspace_probe
   inputs: {absent: discard.txt, read: identity}
outputs:
 first: "{{ sequence.first.outputs.head }}"
 next: "{{ sequence.next.outputs.head }}"
 selected: "{{ vars.selected }}"
`, b, b)
	out, arts := runWorkspaceRecipe(t, ws, yaml, job)
	require.Equal(t, initial, out["first"])
	require.Equal(t, initial, out["next"])
	require.Equal(t, "B", out["selected"])
	require.Nil(t, findThinPack(arts))
}
func TestWorkspaceStateReentryAndCatch(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "A")
	b := filepath.Join(root, "B")
	workspaceRepo(t, a, "A")
	workspaceRepo(t, b, "B")
	ws := workspaceTestWorker(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}, CellResolution: &cellref.Context{Pattern: root + "/${{ cell }}"}}
	out, _ := runWorkspaceRecipe(t, ws, `id: workspace-test
version: "1"
state:
 initial: work
 states:
  work:
   workspace: {cell: B}
   op: workspace_probe
   vars: {selected: "{{ context.workspace.cell }}"}
   inputs: {absent: b.txt, write: b.txt}
   transitions:
    - to: again
  again:
   op: workspace_probe
   inputs: {absent: b.txt, write: a.txt}
   transitions:
    - to: work
      when: "!has(states.work.runs) || size(states.work.runs) < 1"
    - to: failure
  failure:
   workspace: {cell: B}
   op: workspace_probe
   inputs: {fail: true, write: failed.txt}
   catch:
    - when: true
      to: done
  done:
   op: workspace_probe
   inputs: {read: a.txt, absent: failed.txt}
outputs:
 cell: "{{ states.done.outputs.cell }}"
 text: "{{ states.done.outputs.text }}"
`, job)
	require.Equal(t, "A", out["cell"])
	require.Equal(t, "A data", out["text"])
}

func TestWorkspaceExplicitChildrenRestoreSnapshotWithoutAdoptingChildChanges(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "A")
	b := filepath.Join(root, "B")
	workspaceRepo(t, a, "A")
	workspaceRepo(t, b, "B")
	workspaceTestWorker(t)
	child := `id: child
version: "1"
sequence:
 - id: read
   op: workspace_probe
   inputs: {read: b.txt, write: child.txt}
outputs:
 text: "{{ sequence.read.outputs.text }}"
 cell: "{{ sequence.read.outputs.cell }}"
`
	require.NoError(t, os.MkdirAll(filepath.Join(a, ".c2j", "recipes"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(a, ".c2j", "recipes", "child.yaml"), []byte(child), 0644))
	require.NoError(t, runGit(a, "git", "add", "."))
	require.NoError(t, runGit(a, "git", "commit", "-m", "child recipe"))
	eng := newToyEngine(t, "workspace-tests", nil)
	ctl := &workerworkflow.SWFWorkflowControl{Engine: eng}
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{})})
	require.NoError(t, err)
	require.NoError(t, eng.RegisterWorkers(ws))
	parent, err := recipe.LoadRecipeFromString([]byte(`id: parent
version: "1"
workspace: {cell: B}
sequence:
 - id: write
   op: workspace_probe
   inputs: {write: b.txt}
 - id: child
   op: recipe.run_and_get_result
   inputs: {name: child}
 - id: children
   child_group:
    mode: run_and_get_result
    children:
     - key: nested
       recipe: child
 - id: after
   op: workspace_probe
   inputs: {read: b.txt, absent: child.txt}
outputs:
 text: "{{ sequence.after.outputs.text }}"
 child: "{{ sequence.child.outputs.outputs.text }}"
 child_cell: "{{ sequence.child.outputs.outputs.cell }}"
 ok: "${{ sequence.children.outputs.ok }}"
`))
	require.NoError(t, err)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}, CellResolution: &cellref.Context{Pattern: root + "/${{ cell }}"}}
	key, err := starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "workspace-tests", RecipeName: "parent", JobContext: job, GitRef: "main"}, eng, *parent)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(context.Background(), 15*time.Second, key, eng))
	result, err := swfutil.JobResult(context.Background(), eng, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, "B data", out["text"])
	require.Equal(t, "B data", out["child"])
	require.Equal(t, "B", out["child_cell"])
	require.Equal(t, true, out["ok"])
}
