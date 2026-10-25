package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestThinpackRefreshDefaultsPersistenceAndReplay(t *testing.T) {
	for _, tc := range []struct{ pending, included, scoped bool }{{}, {pending: true}, {pending: true, included: true}, {pending: true, scoped: true}, {scoped: true}} {
		t.Run(fmt.Sprintf("pending_%t_included_%t_scoped_%t", tc.pending, tc.included, tc.scoped), func(t *testing.T) {
			pending := tc.pending
			ctx := context.Background()
			repo := filepath.Join(t.TempDir(), "repo")
			initial := workspaceRepo(t, repo, "root")
			type probeInput struct {
				Base  string `json:"base" default:"{{ context.git.execution.base_hash }}" validate:"required,hexadecimal,len=40"`
				Head  string `json:"head" default:"{{ context.git.execution.head_hash }}" validate:"required,hexadecimal,len=40"`
				Early string `json:"early" default:"{{ context.git.resolved_hash }}"`
			}
			calls := map[string]int{}
			target := ""
			advance := func(name string) (string, error) {
				if e := os.WriteFile(filepath.Join(repo, name), []byte(name), 0644); e != nil {
					return "", e
				}
				if e := runGit(repo, "git", "add", "."); e != nil {
					return "", e
				}
				if e := runGit(repo, "git", "commit", "-m", name); e != nil {
					return "", e
				}
				return common.GetCommitHash(ctx, repo, "HEAD")
			}

			makeProbe := func(name string) coreops.RegisterableOp {
				return coreops.NewActivityMappedOpV2[probeInput, map[string]any](coreops.OpMetadata{Type: name}, func(deps coreops.OpDependencies, ctx context.Context, in probeInput) (map[string]any, error) {
					calls[name]++
					actual, e := common.GetCommitHash(ctx, deps.WorktreePath(), "HEAD")
					if e != nil {
						return nil, e
					}
					if in.Head != actual || in.Base != deps.GitContext().ResolvedBaseHash {
						return nil, fmt.Errorf("execution defaults do not match restored workspace")
					}
					switch name {
					case "refresh_seed":
						if in.Early != "" {
							return nil, fmt.Errorf("job base pinned before mutation")
						}
						if pending {
							if e = os.WriteFile(filepath.Join(deps.WorktreePath(), "pending"), []byte("local"), 0644); e != nil {
								return nil, e
							}
						}
					case "refresh_advance":
						if tc.included {
							if e = os.WriteFile(filepath.Join(repo, "pending"), []byte("local"), 0644); e != nil {
								return nil, e
							}
						}
						target, e = advance("child-change")
						if e != nil {
							return nil, e
						}
					case "refresh_after":
						if in.Base != target {
							return nil, fmt.Errorf("lost refreshed base: %s vs %s", in.Base, target)
						}
						if _, e = os.Stat(filepath.Join(deps.WorktreePath(), "child-change")); e != nil {
							return nil, e
						}
						if _, e = os.Stat(filepath.Join(deps.WorktreePath(), "later")); !os.IsNotExist(e) {
							return nil, fmt.Errorf("refresh did not pin completed base")
						}
						if pending {
							b, e := os.ReadFile(filepath.Join(deps.WorktreePath(), "pending"))
							if e != nil {
								return nil, e
							}
							if string(b) != "local" {
								return nil, fmt.Errorf("lost pending work")
							}
						}
					}
					return map[string]any{"base": in.Base, "head": in.Head, "target": target}, nil
				})
			}
			withRegisteredOps(t, thinpackrebase.GetOp(), makeProbe("refresh_seed"), makeProbe("refresh_advance"), makeProbe("refresh_after"))
			registry, e := workerops.NewActivityRegistry()
			require.NoError(t, e)
			refreshCalls := 0
			for name, reg := range registry.GetAll() {
				if reg.Metadata.Type == "thinpackrebase" {
					invoke := reg.Step.Invoke
					reg.Step.Invoke = func(d coreops.OpDependencies, c context.Context, i map[string]any) (map[string]any, error) {
						refreshCalls++
						return invoke(d, c, i)
					}
					registry.UpdateRegistration(name, reg)
				}
			}
			ws, e := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
			require.NoError(t, e)
			rt := toyruntime.New()
			guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
			eng, e := jobworkflow.NewEngineBuilder().WithRuntime(guard).WithWorkerTenantId("refresh").PlusWorkers(ws.JobWorker).BuildEngine()
			require.NoError(t, e)
			eng = jobdbschema.WorkflowEngine{Engine: eng, Registry: rt}
			rec, e := recipe.LoadRecipeFromString([]byte(`id: refresh
sequence:
 - id: seed
   op: refresh_seed
 - id: advance
   op: refresh_advance
 - id: refresh
   op: thinpackrebase
   inputs:
     target_base_hash: '${{ sequence.advance.outputs.target }}'
 - id: after
   op: refresh_after
outputs:
 refresh: '${{ sequence.refresh.outputs }}'
 after: '${{ sequence.after.outputs }}'
`))
			require.NoError(t, e)
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}, GitBase: contextual.GitBaseContext{BaseRepo: repo, BaseRef: "main"}}
			if tc.scoped {
				job.Workspace = &contextual.WorkspaceContext{Cell: "root", ScopeID: "refresh-scope", InitialHash: initial}
			}
			keyRef, e := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "refresh", RecipeName: "refresh", GitRef: "main", JobContext: job}, eng, *rec)
			key := keyRef.Key()
			require.NoError(t, e)
			var first, all []jobworkflow.TaskWorker
			for name, w := range ws.TaskWorkers {
				w = guard.WrapTaskWorker(w)
				all = append(all, w)
				if name != "refresh_after:refresh_after" {
					first = append(first, w)
				}
			}
			run := func(workers []jobworkflow.TaskWorker) jobworkflow.JobRunOutcome {
				h, e := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: workers, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, e)
				out, e := h.Run(nil)
				require.NoError(t, e)
				return out
			}
			firstRun := run(first)
			require.Equal(t, jobworkflow.JobRunSuspended, firstRun.Status, "%v", firstRun.JobError)
			require.NotEqual(t, initial, target)
			require.Equal(t, 1, refreshCalls)
			_, e = advance("later")
			require.NoError(t, e)
			done := run(all)
			require.Equal(t, jobworkflow.JobRunCompleted, done.Status, "%v", done.JobError)
			require.Equal(t, map[string]int{"refresh_seed": 1, "refresh_advance": 1, "refresh_after": 1}, calls)
			require.Equal(t, 1, refreshCalls)
			result, e := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, e)
			raw, e := result.GetData()
			require.NoError(t, e)
			var values map[string]map[string]any
			require.NoError(t, json.Unmarshal(raw, &values))
			require.Equal(t, target, values["refresh"]["new_base_hash"])
			require.Equal(t, values["refresh"]["new_persist_hash"], values["after"]["head"])
			// Completed replay must not inspect Git or rerun the operation.
			require.NoError(t, os.RemoveAll(repo))
			replay, e := eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, e)
			replayRaw, e := replay.GetData()
			require.NoError(t, e)
			require.JSONEq(t, string(raw), string(replayRaw))
			require.Equal(t, 1, refreshCalls)
		})
	}
}
