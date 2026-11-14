package compiler

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/repository"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type legacyAbsentInterruption struct {
	Key    jobdb.JobKey
	Repo   string
	Output map[string]any
}

// This generator must run in the old-compiler overlay binary. The database is
// copied at the interruption, before the old compiler resumes for the oracle.
func TestGenerateAbsentHookLegacyInterruptedFixtures(t *testing.T) {
	dir := os.Getenv("C2J_GENERATE_ABSENT_HOOK_FIXTURES")
	if dir == "" {
		t.Skip("fixture generation is explicit and requires the legacy compiler")
	}
	dir = filepath.Join(dir, "interrupted")
	require.NoError(t, os.MkdirAll(dir, 0755))
	for _, shape := range []string{"sequence", "state"} {
		for _, frontier := range []string{"load", "workspace"} {
			t.Run(shape+"-"+frontier, func(t *testing.T) {
				name := shape + "-" + frontier
				repo := filepath.Join(t.TempDir(), "source")
				workspaceRepo(t, repo, "legacy")
				publications := 0
				registerLegacyAbsentWorkers(t, &publications)
				path := filepath.Join(t.TempDir(), "jobs.db")
				rt, err := sqlite.NewFromConfig(testBackground(), sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20})
				require.NoError(t, err)
				// Canonical ownership is durable; only the embedding's clone transport
				// changes when the bundled source is restored at another temporary path.
				repoID := celltest.Repo(repo)
				key := submitGitLifecycle(t, rt, repoID, legacyAbsentInterruptedRecipe(shape == "state"))
				match := `:load"`
				if frontier == "workspace" {
					match = `task:"workspace:`
				}
				fault := &gitOutcomeInterruption{WorkflowRuntime: rt, match: match, includeBase: true}
				_, err = runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				require.Equal(t, 1, publications)
				history, err := jobdb.GetJobRun(testBackground(), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
				require.NoError(t, err)
				raw, err := json.Marshal(history)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "skip_absent_workspace", "generate with the authentic old compiler")
				require.NoError(t, rt.Close(testBackground()))
				gzipLegacyDatabase(t, path, filepath.Join(dir, name+".db.gz"))
				// The old compiler supplies exact expected context and workspace counters.
				rt, err = sqlite.NewFromConfig(testBackground(), sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20})
				require.NoError(t, err)
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				expected := gitLifecycleOutput(t, out)
				require.Equal(t, 1, publications)
				require.NoError(t, rt.Close(testBackground()))
				info, err := json.MarshalIndent(legacyAbsentInterruption{Key: key, Repo: repoID, Output: expected}, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, name+".json"), info, 0644))
				require.NoError(t, runGit(repo, "git", "bundle", "create", filepath.Join(dir, name+".bundle"), "--all"))
			})
		}
	}
}

func registerLegacyAbsentWorkers(t *testing.T, publications *int) {
	t.Helper()
	op := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "legacy_absent_publication"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
		(*publications)++
		return map[string]any{"published": true}, nil
	})
	workspaceTestWorker(t, op)
}

func gzipLegacyDatabase(t *testing.T, source, target string) {
	t.Helper()
	src, err := os.Open(source)
	require.NoError(t, err)
	defer src.Close()
	dst, err := os.Create(target)
	require.NoError(t, err)
	zipped := gzip.NewWriter(dst)
	_, err = io.Copy(zipped, src)
	require.NoError(t, err)
	require.NoError(t, zipped.Close())
	require.NoError(t, dst.Close())
}

func legacyAbsentInterruptedRecipe(state bool) string {
	hook := `workspace: {cell: test}
inputs: {value: include-bound}
include:
 recipe: cell://.c2/hooks/check.yaml
 if_missing:
  outputs:
   status: skipped
   inputs: '${{ inputs }}'
   cell: '${{ context.workspace.cell }}'
   scope: '${{ context.workspace.scope_id }}'
   hash: '${{ context.git.hash }}'
   repo: '${{ context.git.repo }}'
`
	call := " - id: hook\n" + indentAbsent(hook, 3) + "\n"
	result := "sequence.hook.outputs"
	if state {
		call = " - id: machine\n   state:\n    initial: hook\n    states:\n     hook:\n" + indentAbsent(hook, 6) + "\n   outputs: {result: '${{ states.hook.outputs }}'}\n"
		result = "sequence.machine.outputs.result"
	}
	return `id: git-lifecycle
sequence:
 - id: publication
   op: legacy_absent_publication
` + call + ` - id: later
   workspace: {cell: test}
   op: workspace_probe
outputs:
 result: '${{ ` + result + ` }}'
 later_scope: '${{ sequence.later.outputs.scope }}'
 later_head: '${{ sequence.later.outputs.head }}'
`
}

func TestHookAbsentLegacyInterruptedResumeAndRestart(t *testing.T) {
	for _, shape := range []string{"sequence", "state"} {
		for _, frontier := range []string{"load", "workspace"} {
			t.Run(shape+"-"+frontier, func(t *testing.T) {
				name := shape + "-" + frontier
				dir := "testdata/absent-hook-workspace/interrupted"
				var fixture legacyAbsentInterruption
				raw, err := os.ReadFile(filepath.Join(dir, name+".json"))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &fixture))
				tmp := t.TempDir()
				repo := filepath.Join(tmp, "source")
				bundle, err := filepath.Abs(filepath.Join(dir, name+".bundle"))
				require.NoError(t, err)
				require.NoError(t, runGit(tmp, "git", "clone", bundle, repo))
				// A newly present branch-tip hook must not replace the recorded absence.
				require.NoError(t, runGit(repo, "git", "config", "user.name", "Test"))
				require.NoError(t, runGit(repo, "git", "config", "user.email", "test@example.com"))
				tip := commitHookFile(t, repo, ".c2/hooks/check.yaml", "id: present\nsequence: []\noutputs: {status: present}\n")
				// Only the fresh floating later workspace follows the advanced tip.
				fixture.Output["later_head"] = tip
				ctx := repository.WithRepositoryTransport(celltest.Context(context.Background()), func(id string) string {
					if id == fixture.Repo {
						return repo
					}
					return ""
				})
				path := filepath.Join(tmp, "jobs.db")
				src, err := os.Open(filepath.Join(dir, name+".db.gz"))
				require.NoError(t, err)
				defer src.Close()
				unzip, err := gzip.NewReader(src)
				require.NoError(t, err)
				defer unzip.Close()
				dst, err := os.Create(path)
				require.NoError(t, err)
				_, err = io.Copy(dst, unzip)
				require.NoError(t, err)
				require.NoError(t, dst.Close())
				rt, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path, MaxInlineArtifactBytes: 10 << 20})
				require.NoError(t, err)
				defer rt.Close(ctx)
				publications := 0
				registerLegacyAbsentWorkers(t, &publications)
				before, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: fixture.Key, IncludeInputs: true, IncludeOutputs: true})
				require.NoError(t, err)
				raw, err = json.Marshal(before)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "skip_absent_workspace")
				// Verify the fixture is interrupted after old load, before fallback/later.
				workspaceCount := 0
				if frontier == "workspace" {
					workspaceCount = 1
				}
				assertLegacyAbsentTasks(t, before, workspaceCount)
				out, err := runGitLifecycleContext(t, ctx, rt, fixture.Key)
				require.NoError(t, err)
				require.Equal(t, fixture.Output, gitLifecycleOutput(t, out))
				require.Zero(t, publications, "the old compiler already published exactly once")
				history, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: fixture.Key, IncludeInputs: true, IncludeOutputs: true})
				require.NoError(t, err)
				assertLegacyAbsentTasks(t, history, 2)
				fallback := fixture.Output["result"].(map[string]any)
				require.Equal(t, "test", fallback["cell"], "legacy fallback retains the include workspace cell")
				require.Equal(t, "https://"+fixture.Repo+".git", fallback["repo"])
				require.Equal(t, "", fallback["hash"], "legacy floating workspace context")
				require.Equal(t, map[string]any{}, fallback["inputs"], "legacy synthetic body does not forward explicit include inputs")
				require.NotEmpty(t, fallback["scope"])
				require.NotEqual(t, fallback["scope"], fixture.Output["later_scope"])
				// Both boundaries retain the old load and publication. Restarting the
				// wrapper also exercises workspace-counter reconstruction before later.
				scopes := map[string]string{}
				for _, entry := range history.Execution {
					if entry.Record.Kind != "scope" {
						continue
					}
					for label, suffix := range map[string]string{"hook": `scope:"sequence:hook"#1`, "later": `scope:"op:later"#1`} {
						if strings.HasSuffix(entry.Record.ID, suffix) {
							scopes[label] = entry.Record.ID
						}
					}
				}
				require.Len(t, scopes, 2)
				for _, label := range []string{"hook", "later"} {
					t.Run("restart-"+label, func(t *testing.T) {
						restart, err := rt.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: fixture.Key, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: scopes[label]}, Cell: "test"}})
						require.NoError(t, err)
						out, err := runGitLifecycleContext(t, ctx, rt, restart.JobKey)
						require.NoError(t, err)
						require.Equal(t, fixture.Output, gitLifecycleOutput(t, out))
						require.Zero(t, publications)
						restarted, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: restart.JobKey, IncludeInputs: true, IncludeOutputs: true})
						require.NoError(t, err)
						assertLegacyAbsentTasks(t, restarted, 2)
						// Completed replay uses recorded data and does not change chapters.
						chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: restart.JobKey})
						require.NoError(t, err)
						engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(fixture.Key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
						require.NoError(t, err)
						replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: restart.JobKey})
						require.NoError(t, err)
						require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(replay.GetDataOrPanic()))
						after, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: restart.JobKey})
						require.NoError(t, err)
						require.Equal(t, chapters, after)
						require.Zero(t, publications)
					})
				}
			})
		}
	}
}

func assertLegacyAbsentTasks(t *testing.T, run jobdb.GetJobRunResponse, workspaces int) {
	t.Helper()
	raw, err := json.Marshal(run)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "skip_absent_workspace")
	resolutions, publication, workspace := 0, 0, 0
	for _, task := range executionTaskRuns(run) {
		switch task.TaskType {
		case hookResolveTaskType:
			resolutions++
		case "legacy_absent_publication:legacy_absent_publication":
			publication++
		case WorkspaceResolutionTaskType:
			workspace++
		}
	}
	require.Equal(t, 2, resolutions, "one old pin and load, without rediscovery")
	require.Equal(t, 1, publication, "exactly one durable publication sentinel")
	require.Equal(t, workspaces, workspace, "legacy include and later workspace entry")
}
