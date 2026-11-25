package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/parentagetest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLeaseParentageWorkerCLI(t *testing.T) {
	binary := os.Getenv("C2J_TEST_SUBMIT_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		cmd := exec.Command("go", "build", "-mod=readonly", "-o", binary, "../../../cmd/c2j")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, extension := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/extension=%t", backend, extension), func(t *testing.T) {
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "source")
				childRecipe := `id: child
version: "1"
input_schema:
 prompt: {type: string}
sequence: []
outputs: {}
`
				commitHookFile(t, repo, "child.yaml", childRecipe)
				captures := t.TempDir()
				run := fmt.Sprintf(`set -eu; n=1; test ! -f %q/child-1.json || n=2; %q submit 'child from worker' --cell test --advanced-recipe-file %q --job-id "cli-child-$n" --json > %q/child-$n.json; cat %q/child-$n.json`, captures, binary, filepath.Join(repo, "child.yaml"), captures, captures)
				workspaceTestWorker(t, commandop.GetOp(), extops.GetExecutionOp())
				op := "command_execution"
				inputs := fmt.Sprintf("   inputs: {run: %q, timeout: 30s}\n", run)
				if extension {
					commitHookFile(t, repo, "submit/op.yaml", fmt.Sprintf("name: submit\nshell: sh\ntimeout: 30s\nrun: %q\ninput_schema: {type: object}\noutput_schema: {type: object}\n", run))
					op = "./submit"
					inputs = ""
				}
				yaml := fmt.Sprintf(`id: parentage
version: "1"
sequence:
 - id: first
   op: %s
   perms: {jobdb: {jobs: {submit: true}}}
%s - id: second
   op: %s
   perms: {jobdb: {jobs: {submit: true}}}
%soutputs: {}
`, op, inputs, op, inputs)
				rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
				require.NoError(t, jobdbschema.Register(celltest.Context(context.Background()), rt.(jobdb.JobSchemaRegistry), "git-tests"))
				key := submitGitLifecycle(t, rt, repo, yaml)
				outcome, err := runParentageLifecycle(t, rt, key)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
				ctx := celltest.Context(context.Background())
				parentagetest.Check(t, ctx, rt, key, "cli-child-1", "cli-child-2")
				hashes := []string{}
				for n := 1; n <= 2; n++ {
					raw, err := os.ReadFile(filepath.Join(captures, fmt.Sprintf("child-%d.json", n)))
					require.NoError(t, err)
					var receipt struct {
						JobID string `json:"job_id"`
					}
					require.NoError(t, json.Unmarshal(raw, &receipt))
					require.Equal(t, fmt.Sprintf("cli-child-%d", n), receipt.JobID)
					childKey := jobdb.JobKey{TenantId: key.TenantId, JobId: receipt.JobID}
					chapter, err := rt.GetChapter(ctx, jobdb.ChapterRef{JobKey: childKey, Ordinal: 0})
					require.NoError(t, err)
					var start workflowctl.StartJob
					require.NoError(t, json.Unmarshal(chapter.Body.(jobdb.JobStartChapter).Input.Data, &start))
					require.NotNil(t, start.Parent)
					require.Equal(t, key.JobId, start.Parent.JobID)
					require.NotEmpty(t, start.Parent.InvocationHash)
					hashes = append(hashes, start.Parent.InvocationHash)
					// Completed children remain visible to formal queries and the DAG.
					childOutcome, err := runParentageLifecycle(t, rt, childKey)
					require.NoError(t, err)
					require.Equal(t, wf.JobRunCompleted, childOutcome.Status, "%v", childOutcome.JobError)
				}
				require.NotEqual(t, hashes[0], hashes[1], "separate launching invocations")
				parentagetest.Check(t, ctx, rt, key, "cli-child-1", "cli-child-2")
			})
		}
	}
}

// Use the normal runtime capability detection: embedded runtimes guard calls
// in-process; remote runtimes additionally issue restricted delegations.
func runParentageLifecycle(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey) (wf.JobRunOutcome, error) {
	t.Helper()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	engine, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: engine}).Build()
	ws, err := NewRecipeWorker(deps, registry)
	require.NoError(t, err)
	guard := executionruntime.New(rt, execution.Allocation{SchemaVersion: 1})
	tasks := make([]wf.TaskWorker, 0, len(ws.TaskWorkers))
	for _, task := range ws.TaskWorkers {
		tasks = append(tasks, guard.WrapTaskWorker(task))
	}
	runnable, err := wf.GetJobForRun(celltest.Context(context.Background()), guard, wf.GetJobForRunRequest{JobKey: key, JobWorker: ws.JobWorker, TaskWorkers: tasks, WorkerID: "parentage", LeaseDuration: time.Minute})
	if err != nil {
		return wf.JobRunOutcome{}, err
	}
	return runnable.Run(nil)
}

func TestSubmitterDependencyTaskContext(t *testing.T) {
	binary := os.Getenv("C2J_TEST_SUBMIT_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		out, err := exec.Command("go", "build", "-mod=readonly", "-o", binary, "../../../cmd/c2j").CombinedOutput()
		require.NoError(t, err, string(out))
	}
	for _, mode := range []string{"sequence", "states"} {
		t.Run(mode, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			commitHookFile(t, repo, "child.yaml", "id: child\nversion: '1'\ninput_schema: {prompt: {type: string}}\nsequence: []\noutputs: {}\n")
			submit := fmt.Sprintf(`%q submit 'child from task' --cell test --advanced-recipe-file %q --json --job-id`, binary, filepath.Join(repo, "child.yaml"))
			run := "set -eu\n" + submit + " child-true --submitter-dependency=true\n" + submit + " child-false --submitter-dependency=false\n" + submit + " child-omitted"
			sibling := submit + " child-sibling --submitter-dependency=true"
			node := func(header, command string) string {
				return fmt.Sprintf(`%s
   op: command_execution
   perms: {jobdb: {jobs: {submit: true}}}
   inputs: {run: %q, timeout: 30s}
`, header, command)
			}
			yaml := "id: dependency_marker\nversion: '1'\n"
			if mode == "sequence" {
				yaml += "sequence:\n" + node(" - id: launch", run) + node(" - id: sibling", sibling)
			} else {
				yaml += "state:\n initial: launch\n states:\n" + node("  launch:", run) +
					"   transitions: [{to: sibling}]\n" + node("  sibling:", sibling)
			}
			launch, other := mode+".launch.jobs.items", mode+".sibling.jobs.items"
			yaml += fmt.Sprintf(`outputs:
 items: '${{ %s }}'
 included: '${{ %s.filter(item, !has(item.submitter_dependency) || item.submitter_dependency).map(item, item.job_id) }}'
 sibling: '${{ %s.map(item, item.job_id) }}'
`, launch, launch, other)
			workspaceTestWorker(t, commandop.GetOp())
			rt := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
			ctx := celltest.Context(context.Background())
			require.NoError(t, jobdbschema.Register(ctx, rt.(jobdb.JobSchemaRegistry), "git-tests"))
			key := submitGitLifecycle(t, rt, repo, yaml)
			outcome, err := runParentageLifecycle(t, rt, key)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, outcome.Status, "%v", outcome.JobError)

			want := map[string]string{"child-true": "true", "child-false": "false", "child-omitted": ""}
			children, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{key.TenantId}, ParentJobIDs: []string{key.JobId}})
			require.NoError(t, err)
			require.Len(t, children.Jobs, 4)
			stored := map[string]jobdb.JobSummary{}
			for _, row := range children.Jobs {
				stored[row.JobKey.JobId] = row
				var meta map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(row.Metadata, &meta))
				expected := want[row.JobKey.JobId]
				if row.JobKey.JobId == "child-sibling" {
					expected = "true"
				}
				require.Equal(t, expected, string(meta["submitter_dependency"]), row.JobKey.JobId)
			}
			assertItems := func(items []map[string]json.RawMessage) {
				t.Helper()
				got := map[string]string{}
				for _, item := range items {
					var id string
					require.NoError(t, json.Unmarshal(item["job_id"], &id))
					row, ok := stored[id]
					require.True(t, ok, id)
					raw, err := json.Marshal(row.JobKey.TenantId)
					require.NoError(t, err)
					require.JSONEq(t, string(raw), string(item["tenant_id"]))
					require.NotContains(t, item, "goal_id")
					require.NotContains(t, got, id)
					got[id] = string(item["submitter_dependency"])
				}
				require.Equal(t, want, got)
			}
			var output struct {
				Items             []map[string]json.RawMessage
				Included, Sibling []string
			}
			require.NoError(t, json.Unmarshal(outcome.Output.GetDataOrPanic(), &output))
			assertItems(output.Items)
			require.ElementsMatch(t, []string{"child-true", "child-omitted"}, output.Included)
			require.Equal(t, []string{"child-sibling"}, output.Sibling)

			history, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true})
			require.NoError(t, err)
			tasks, err := swfutil.ExecutionTasks(history)
			require.NoError(t, err)
			launches, siblings := 0, 0
			for _, task := range tasks {
				if !strings.HasPrefix(task.TaskType, "command_execution:") {
					continue
				}
				for _, attempt := range task.Attempts {
					if attempt.Output == nil {
						continue
					}
					decoded, err := decodeTaskOutput(attempt.Output.Data)
					require.NoError(t, err)
					jobs := decoded.Activity.Jobs
					switch len(jobs.Items) {
					case 3:
						launches++
						require.Empty(t, jobs.GoalIDs)
						for i, item := range jobs.Items {
							row := stored[item.JobID]
							require.Equal(t, item.JobID, jobs.JobIDs[i])
							require.Equal(t, row.Cell, item.Cell)
							require.Equal(t, row.CellID, item.CellID)
							require.Equal(t, row.CellRepo, item.CellRepo)
						}
						raw, err := json.Marshal(jobs.Items)
						require.NoError(t, err)
						var items []map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(raw, &items))
						assertItems(items)
					case 1:
						siblings++
						require.Equal(t, "child-sibling", jobs.Items[0].JobID)
					default:
						t.Fatalf("unexpected persisted children: %+v", jobs)
					}
				}
			}
			require.Equal(t, 1, launches)
			require.Equal(t, 1, siblings)
		})
	}
}
