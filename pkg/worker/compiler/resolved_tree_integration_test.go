package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type resolvedCountedOp struct {
	coreops.RegisterableOp
	invoke func(coreops.OpDependencies, context.Context, map[string]any, func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error)
}

func (op resolvedCountedOp) TaskChain() []coreops.TaskStep {
	chain := append([]coreops.TaskStep(nil), op.RegisterableOp.TaskChain()...)
	for i := range chain {
		native := chain[i].Invoke
		chain[i].Invoke = func(d coreops.OpDependencies, ctx context.Context, input map[string]any) (map[string]any, error) {
			return op.invoke(d, ctx, input, native)
		}
	}
	return chain
}

func resolvedHash(t *testing.T, repo, ref string) string {
	t.Helper()
	h, err := common.GetCommitHash(celltest.Context(context.Background()), repo, ref)
	require.NoError(t, err)
	return h
}

func resolvedCommit(t *testing.T, repo, message string) string {
	t.Helper()
	require.NoError(t, runGit(repo, "git", "add", "-A"))
	require.NoError(t, runGit(repo, "git", "-c", "user.name=Resolver", "-c", "user.email=resolver@example.com", "commit", "-m", message))
	return resolvedHash(t, repo, "HEAD")
}

func resolvedFixture(t *testing.T) (repo, base, target string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "source")
	workspaceRepo(t, repo, "base")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base-marker"), []byte("authoritative base"), 0644))
	base = resolvedCommit(t, repo, "managed base after earlier ancestor")
	require.NoError(t, runGit(repo, "git", "checkout", "-b", "pinned-target"))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "identity"), []byte("upstream change\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "target-only"), []byte("deliberately omitted by resolution\n"), 0644))
	target = resolvedCommit(t, repo, "target")
	require.NoError(t, runGit(repo, "git", "checkout", "main"))
	return
}

func resolvedObserve(t *testing.T, d coreops.OpDependencies, ctx context.Context) map[string]any {
	t.Helper()
	repo, g := d.WorktreePath(), d.GitContext()
	status, err := common.ExecuteGitCommand(ctx, repo, "status", "--porcelain=v1", "-z")
	require.NoError(t, err)
	require.Empty(t, status)
	for _, state := range []string{"MERGE_HEAD", "rebase-apply", "rebase-merge", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer"} {
		p, err := common.ExecuteGitCommand(ctx, repo, "rev-parse", "--git-path", state)
		require.NoError(t, err)
		path := strings.TrimSpace(string(p))
		if !filepath.IsAbs(path) {
			path = filepath.Join(repo, path)
		}
		require.NoFileExists(t, path)
		require.NoDirExists(t, path)
	}
	parents, err := common.ExecuteGitCommand(ctx, repo, "show", "-s", "--format=%P", "HEAD")
	require.NoError(t, err)
	diff, err := common.ExecuteGitCommand(ctx, repo, "diff", "--unified=10", g.ResolvedBaseHash, "HEAD")
	require.NoError(t, err)
	return map[string]any{"head": resolvedHash(t, repo, "HEAD"), "tree": resolvedHash(t, repo, "HEAD^{tree}"),
		"base": g.ResolvedBaseHash, "accepted": g.PersistHash, "parents": strings.TrimSpace(string(parents)), "dir": repo,
		"restore": g.RestoreArtifact, "diff": string(diff)}
}

func resolvedFixtureOps(t *testing.T, target string, equal bool) []coreops.RegisterableOp {
	t.Helper()
	prepare := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "resolved_prepare"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		repo := d.WorktreePath()
		require.NoError(t, os.WriteFile(filepath.Join(repo, "identity"), []byte("older conflicting local commit\n"), 0644))
		resolvedCommit(t, repo, "earlier local conflict")
		require.NoError(t, os.WriteFile(filepath.Join(repo, "identity"), []byte("completed resolution of upstream and local\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(repo, "binary\nname"), []byte{0, 128, 255, 0, 10}, 0644))
		require.NoError(t, os.WriteFile(filepath.Join(repo, "run"), []byte("#!/bin/sh\nexit 0\n"), 0755))
		require.NoError(t, os.Symlink("identity", filepath.Join(repo, "link")))
		resolvedCommit(t, repo, "completed resolution")
		if equal {
			_, err := common.ExecuteGitCommand(ctx, repo, "fetch", "origin", target)
			require.NoError(t, err)
			require.NoError(t, runGit(repo, "git", "read-tree", "--reset", "-u", target))
			resolvedCommit(t, repo, "resolution equals target")
		}
		result := resolvedObserve(t, d, ctx)
		result["confirmation"] = map[string]any{"tree_hash": result["tree"]}
		_, err := common.ExecuteGitCommand(ctx, repo, "fetch", "--depth", "2", "origin", d.GitContext().ResolvedBaseHash)
		require.NoError(t, err)
		result["older_base"] = resolvedHash(t, repo, d.GitContext().ResolvedBaseHash+"^")
		return result, nil
	})
	observe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "resolved_observe"}, func(d coreops.OpDependencies, ctx context.Context, _ struct{}) (map[string]any, error) {
		return resolvedObserve(t, d, ctx), nil
	})
	repair := coreops.NewActivityMappedOpV2[struct {
		Failure  map[string]any `json:"failure"`
		Evidence artifacts.Ref  `json:"evidence"`
	}, map[string]any](coreops.OpMetadata{Type: "resolved_repair"}, func(d coreops.OpDependencies, ctx context.Context, in struct {
		Failure  map[string]any `json:"failure"`
		Evidence artifacts.Ref  `json:"evidence"`
	}) (map[string]any, error) {
		before := resolvedObserve(t, d, ctx)
		details := in.Failure["attrs"].(map[string]any)["rebase"].(map[string]any)
		require.Equal(t, thinpackrebase.ConflictCode, in.Failure["code"])
		require.Equal(t, true, details["workspace_restored"])
		require.EqualValues(t, 1, details["version"])
		require.Equal(t, target, details["target_base_hash"])
		require.Equal(t, before["head"], details["candidate_hash"])
		require.Equal(t, before["base"], details["base_hash"])
		key, ok := in.Evidence.StoredKey()
		require.True(t, ok)
		a, err := d.FindArtifact(key)
		require.NoError(t, err)
		data, err := a.Bytes(ctx)
		require.NoError(t, err)
		require.Contains(t, string(data), "identity")
		require.NoError(t, os.WriteFile(filepath.Join(d.WorktreePath(), "repaired"), []byte("fresh persisted repair\n"), 0644))
		resolvedCommit(t, d.WorktreePath(), "persisted repair after restored conflict")
		after := resolvedObserve(t, d, ctx)
		after["original"] = details["candidate_hash"]
		return after, nil
	})
	return []coreops.RegisterableOp{prepare, observe, repair}
}

func resolvedRecipe(selector, target, route string) string {
	adopt := fmt.Sprintf(`op: %s
inputs:
 base_hash: '${{ sequence.prepare.outputs.base }}'
 persist_hash: '${{ sequence.prepare.outputs.head }}'
 target_base_hash: %s
 resolution: {tree_hash: '${{ sequence.prepare.outputs.tree }}'}
`, selector, target)
	if route == "repair" {
		adopt = fmt.Sprintf(`inputs: {target: %s}
vars: {target: '${{ inputs.target }}'}
state:
 initial: replay
 states:
  replay:
   op: %s
   inputs: {target_base_hash: '${{ vars.target }}'}
   catch:
    - when: failure.kind == 'task_error' && failure.code == 'REBASE_CONFLICT' && failure.attrs.rebase.version == 1 && failure.attrs.rebase.workspace_restored
      to: repair
  repair:
   sequence:
    - id: repair
      op: resolved_repair
      inputs:
       failure: '${{ transition.failure }}'
       evidence: '${{ transition.failure.attrs.rebase.diagnostics["_c2j/rebase/conflict-index.z"] }}'
    - id: adopt
      op: %s
      inputs:
       base_hash: '${{ sequence.repair.outputs.base }}'
       persist_hash: '${{ sequence.repair.outputs.head }}'
       target_base_hash: '${{ vars.target }}'
       resolution: {tree_hash: '${{ sequence.repair.outputs.tree }}'}
   outputs:
    adoption: '${{ sequence.adopt.outputs }}'
    repaired: '${{ sequence.repair.outputs }}'
outputs: '${{ states.repair.outputs }}'
`, target, selector, selector)
	}
	// Mapping outputs are required on composite nodes.
	if route == "repair" {
		adopt = strings.Replace(adopt, "outputs: '${{ states.repair.outputs }}'", "outputs:\n adoption: '${{ states.repair.outputs.adoption }}'\n repaired: '${{ states.repair.outputs.repaired }}'", 1)
	}
	return `id: resolved-tree-lifecycle
version: "1"
sequence:
 - id: prepare
   op: resolved_prepare
 - id: integrate
   ` + strings.ReplaceAll(strings.TrimSpace(adopt), "\n", "\n   ") + `
 - id: observe
   op: resolved_observe
outputs:
 prepared: '${{ sequence.prepare.outputs }}'
 integration: '${{ sequence.integrate.outputs }}'
 restored: '${{ sequence.observe.outputs }}'
`
}

func TestResolvedTreeFreshWorkerPersistenceAndOfflineReplay(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, selector := range []string{"rebase", "thinpackrebase"} {
			for _, route := range []string{"direct", "repair", "equal"} {
				t.Run(backend+"/"+selector+"/"+route, func(t *testing.T) {
					repo, base, target := resolvedFixture(t)
					calls := 0
					var adoptionDir string
					op := thinpackrebase.GetRebaseOp()
					if selector == "thinpackrebase" {
						op = thinpackrebase.GetOp()
					}
					counted := resolvedCountedOp{op, func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
						if _, adoption := input["resolution"]; adoption {
							calls++
							adoptionDir = d.WorktreePath()
						}
						return native(d, ctx, input)
					}}
					workspaceTestWorker(t, append(resolvedFixtureOps(t, target, route == "equal"), counted)...)
					rt := newTimeoutReproRuntime(t, backend)
					key := submitGitLifecycle(t, rt, repo, resolvedRecipe(selector, target, route))
					// Repair mode also interrupts after replay, proving clean repair is
					// reconstructed by another worker from the failed task's history.
					if route == "repair" {
						fault := &gitOutcomeInterruption{ConsumerRuntime: rt, match: `task:"` + selector + `:` + selector + `"`}
						_, err := runGitLifecycle(t, fault, key)
						require.Error(t, err)
						require.True(t, fault.fired)
						require.Zero(t, calls)
					}
					fault := &gitOutcomeInterruption{ConsumerRuntime: rt, match: `task:"` + selector + `:` + selector + `"`}
					if route == "repair" {
						fault.match = `scope:"op:adopt"`
					}
					_, err := runGitLifecycle(t, fault, key)
					require.Error(t, err)
					require.True(t, fault.fired)
					require.Equal(t, 1, calls)
					// Destroy the checkout even if a runtime implementation retains it.
					require.NoError(t, os.RemoveAll(adoptionDir))
					// Upstream advances after confirmation. Resume must retain target.
					require.NoError(t, runGit(repo, "git", "reset", "--hard", target))
					require.NoError(t, os.WriteFile(filepath.Join(repo, "unconfirmed-newer"), []byte("must not adopt"), 0644))
					latest := resolvedCommit(t, repo, "newer remote")
					require.NotEqual(t, target, latest)
					out, err := runGitLifecycle(t, rt, key)
					require.NoError(t, err)
					result := gitLifecycleOutput(t, out)
					prepared := result["prepared"].(map[string]any)
					integration := result["integration"].(map[string]any)
					repaired := prepared
					if route == "repair" {
						repaired = integration["repaired"].(map[string]any)
						integration = integration["adoption"].(map[string]any)
						require.Equal(t, prepared["head"], repaired["original"])
						require.NotEqual(t, prepared["head"], repaired["head"])
					}
					restored := result["restored"].(map[string]any)
					require.Equal(t, target, integration["target_base_hash"])
					require.Equal(t, target, integration["new_base_hash"])
					require.Equal(t, base, integration["rebased_from"].(map[string]any)["base_hash"])
					require.Equal(t, repaired["head"], integration["rebased_from"].(map[string]any)["persist_hash"])
					require.Equal(t, repaired["tree"], restored["tree"])
					require.Equal(t, target, restored["base"])
					require.Equal(t, integration["new_persist_hash"], restored["head"])
					require.Equal(t, restored["head"], restored["accepted"])
					require.NotEqual(t, adoptionDir, restored["dir"])
					require.Equal(t, 1, calls, "durable adoption must not execute again")
					if route == "equal" {
						require.Equal(t, target, restored["head"])
						require.Nil(t, restored["restore"], "obsolete thin pack must be discarded")
					} else {
						require.Equal(t, target, restored["parents"])
					}
					assertResolvedArtifacts(t, rt, key, selector, route == "equal", target, restored["head"].(string), restored["diff"].(string))
					engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{})).BuildEngine()
					require.NoError(t, err)
					require.NoError(t, os.Rename(repo, repo+"-offline"))
					played, err := engine.ReplayJobRun(celltest.Context(context.Background()), jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
					require.NoError(t, err)
					require.JSONEq(t, string(out.Output.GetDataOrPanic()), string(played.GetDataOrPanic()))
					require.Equal(t, 1, calls)
					t.Logf("contract target=%s input_candidate=%v tree=%v output_candidate=%v restored_base=%v", target, repaired["head"], repaired["tree"], integration["new_persist_hash"], restored["base"])
				})
			}
		}
	}
}

func assertResolvedArtifacts(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey, selector string, equal bool, target, candidate, expectedDiff string, parentDiff ...string) {
	t.Helper()
	history, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true})
	require.NoError(t, err)
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	found := false
	for _, task := range executionTaskRuns(history) {
		if task.TaskType != selector+":"+selector {
			continue
		}
		for _, attempt := range task.Attempts {
			if attempt.Output == nil || attempt.Output.Data == nil {
				continue
			}
			output, err := decodeTaskOutput(attempt.Output.Data)
			if err != nil || output.Activity.OpOutput["new_persist_hash"] != candidate {
				continue
			}
			found = true
			require.Equal(t, target, output.Activity.GitResult.ParentHash)
			require.Equal(t, candidate, output.Activity.GitResult.PersistHash)
			if equal {
				for _, artifact := range attempt.Output.Artifacts {
					require.NotEqual(t, gitstate.ThinPackArtifactName, artifact.Name)
					require.False(t, strings.HasSuffix(artifact.Name, ".diff"))
				}
				continue
			}
			names := map[string]bool{}
			for _, artifact := range attempt.Output.Artifacts {
				names[artifact.Name] = true
				require.NotNil(t, artifact.Key)
				data, err := artifact.Key.ToLazyArtifact(engine, key.TenantId).Bytes(celltest.Context(context.Background()))
				require.NoError(t, err)
				require.NotEmpty(t, data)
				if artifact.Name == gitstate.ThinPackArtifactName {
					require.Contains(t, string(data), "-"+target+" ", "bundle must require the accepted target")
				}
				if artifact.Name == "diff_from_base.diff" {
					require.Equal(t, expectedDiff, string(data))
				}
				if artifact.Name == "diff_from_parent.diff" {
					want := expectedDiff
					if len(parentDiff) != 0 {
						want = parentDiff[0]
					}
					require.Equal(t, want, string(data))
				}
			}
			require.True(t, names[gitstate.ThinPackArtifactName])
			require.True(t, names["diff_from_parent.diff"])
			// Single-parent adoption has the target as both parent and base;
			// native persistence writes their shared diff only once.
			if len(parentDiff) == 0 || parentDiff[0] == expectedDiff {
				require.False(t, names["diff_from_base.diff"])
			} else {
				require.True(t, names["diff_from_base.diff"])
			}
		}
	}
	require.True(t, found, "adoption output must be durably recorded")
}

func resolvedJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	return string(b)
}
