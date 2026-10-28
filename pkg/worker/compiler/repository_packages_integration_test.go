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

	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
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

type repositoryPackageInput struct {
	SetVersion string `json:"set_version,omitempty"`
	NoTool     bool   `json:"no_tool,omitempty"`
}
type repositoryPackageOutput struct {
	Version string `json:"version"`
}

func TestRepositoryPackagesSnapshotsWorkspacesChildrenAndReplay(t *testing.T) {
	ctx := context.Background()
	root, bin, cache := t.TempDir(), t.TempDir(), t.TempDir()
	log := filepath.Join(root, "installs")
	t.Setenv("C2J_TOOL_CACHE_DIR", cache)
	t.Setenv("C2J_REPOSITORY_PACKAGE_LOG", log)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
for spec do :; done
printf '%s\n' "$spec" >> "$C2J_REPOSITORY_PACKAGE_LOG"
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\nprintf "%%s\\n" %s\n' "'$spec'" > "$UV_TOOL_BIN_DIR/repo-tool"
chmod +x "$UV_TOOL_BIN_DIR/repo-tool"
`), 0700))
	writeConfig := func(dir, version string) error {
		if err := os.MkdirAll(filepath.Join(dir, ".c2"), 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, ".c2", "config.yaml"), []byte("packages: [uv:repo-tool=="+version+"]\n"), 0600)
	}
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	for _, item := range []struct{ path, version string }{{a, "a"}, {b, "b"}} {
		workspaceRepo(t, item.path, item.version)
		require.NoError(t, writeConfig(item.path, item.version))
		require.NoError(t, runGit(item.path, "git", "add", "."))
		require.NoError(t, runGit(item.path, "git", "commit", "-m", "packages"))
	}
	calls := 0
	probe := coreops.NewActivityMappedOpV2[repositoryPackageInput, repositoryPackageOutput](coreops.OpMetadata{Type: "repository_package_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in repositoryPackageInput) (repositoryPackageOutput, error) {
		calls++
		out := repositoryPackageOutput{}
		if !in.NoTool {
			stdout, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{WorkingDir: deps.WorktreePath(), Command: []string{"repo-tool"}})
			if err != nil {
				return out, fmt.Errorf("repo-tool: %w: %s", err, stderr)
			}
			out.Version = strings.TrimSpace(string(stdout))
		}
		if in.SetVersion != "" {
			return out, writeConfig(deps.WorktreePath(), in.SetVersion)
		}
		return out, nil
	})
	workspaceTestWorker(t, probe)
	require.NoError(t, os.MkdirAll(filepath.Join(a, ".c2/recipes"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(a, ".c2/recipes/child.yaml"), []byte("id: child\nversion: '1'\nop: repository_package_probe\n"), 0600))
	require.NoError(t, runGit(a, "git", "add", "."))
	require.NoError(t, runGit(a, "git", "commit", "-m", "child"))
	eng := newToyEngine(t, "repository-packages", nil)
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: eng}).Build()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{RootSourceResolver: NewRecipeSourceResolver(RecipeSourceResolverOptions{})})
	require.NoError(t, err)
	require.NoError(t, eng.RegisterWorkers(ws))
	rec, err := recipe.LoadRecipeFromString([]byte(`id: repository-packages
version: "1"
sequence:
 - id: first
   op: repository_package_probe
 - id: edit
   op: repository_package_probe
   inputs: {set_version: c}
 - id: updated
   op: repository_package_probe
 - id: b
   workspace: {cell: B}
   op: repository_package_probe
 - id: child
   workspace: {cell: B}
   op: recipe.run_and_get_result
   inputs: {name: child}
 - id: back
   op: repository_package_probe
 - id: override
   execution_needs: {packages: [uv:repo-tool==node]}
   op: repository_package_probe
 - id: empty
   workspace: {cell: E, initial: empty}
   sequence:
    - id: seed
      op: repository_package_probe
      inputs: {no_tool: true, set_version: e}
    - id: use
      op: repository_package_probe
   outputs:
     version: "{{ sequence.use.outputs.version }}"
outputs:
 first: "{{ sequence.first.outputs.version }}"
 edit: "{{ sequence.edit.outputs.version }}"
 updated: "{{ sequence.updated.outputs.version }}"
 b: "{{ sequence.b.outputs.version }}"
 child: "{{ sequence.child.outputs.outputs.version }}"
 back: "{{ sequence.back.outputs.version }}"
 override: "{{ sequence.override.outputs.version }}"
 empty: "{{ sequence.empty.outputs.version }}"
`))
	require.NoError(t, err)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}, CellResolution: &cellref.Context{Pattern: root + "/${{ cell }}", SelfRepo: a, SelfRef: "main"}}
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "repository-packages", RecipeName: "repository-packages", JobContext: job, GitRef: "main"}, eng, *rec)
	require.NoError(t, err)
	key := ref.Key()
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, time.Minute, key, eng))
	result, err := swfutil.JobResult(ctx, eng, key)
	require.NoError(t, err)
	raw, err := result.GetData()
	require.NoError(t, err)
	require.JSONEq(t, `{"first":"repo-tool==a","edit":"repo-tool==a","updated":"repo-tool==c","b":"repo-tool==b","child":"repo-tool==b","back":"repo-tool==c","override":"repo-tool==node","empty":"repo-tool==e"}`, string(raw))
	installed, err := os.ReadFile(log)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"repo-tool==a", "repo-tool==b", "repo-tool==c", "repo-tool==node", "repo-tool==e"}, strings.Fields(string(installed)))
	history, err := eng.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
	require.NoError(t, err)
	seen := 0
	for _, task := range executionTaskRuns(history) {
		for _, attempt := range task.Attempts {
			if attempt.Output == nil || !strings.Contains(task.TaskType, "repository_package_probe") {
				continue
			}
			decoded, err := decodeTaskOutput(attempt.Output.Data)
			require.NoError(t, err)
			diagnostic := decoded.Activity.Setup.Repository
			require.NotNil(t, diagnostic)
			if !diagnostic.Missing {
				require.NotEmpty(t, diagnostic.Digest)
				require.NotEmpty(t, diagnostic.Snapshot)
			}
			seen++
		}
	}
	require.Greater(t, seen, 5)
	before := calls
	// No restored workspace, config, manager, or installation remains available.
	require.NoError(t, os.RemoveAll(a))
	require.NoError(t, os.RemoveAll(b))
	require.NoError(t, os.RemoveAll(cache))
	require.NoError(t, os.RemoveAll(bin))
	replay, err := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	replayed, err := replay.GetData()
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(replayed))
	require.Equal(t, before, calls)
	// Keep a type check here because these diagnostics are part of durable JSON.
	var values map[string]string
	require.NoError(t, json.Unmarshal(replayed, &values))
}
