package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type workspaceReplayEntry struct {
	task  string
	input []byte
	out   jobdb.TaskData
}
type workspaceReplayContext struct {
	noLeaseClientContext
	entries  []workspaceReplayEntry
	position int
	resolves int
}

func (*workspaceReplayContext) GetJobKey() jobdb.JobKey {
	return jobdb.JobKey{TenantId: "tenant", JobId: "workspace-replay"}
}
func (*workspaceReplayContext) Logger() *slog.Logger               { return slog.Default() }
func (*workspaceReplayContext) AwaitJobs(...string) error          { return nil }
func (*workspaceReplayContext) AwaitDuration(jobdb.Duration) error { return nil }
func (c *workspaceReplayContext) DoTask(_ jobdb.RunPolicy, task string, data jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	pos := c.position
	c.position++
	if pos < len(c.entries) {
		entry := c.entries[pos]
		if task != entry.task || string(raw) != string(entry.input) {
			return nil, fmt.Errorf("non-deterministic task at %d: %s\n%s\n%s", pos, task, entry.input, raw)
		}
		return entry.out, nil
	}
	var out jobdb.TaskData
	if task == WorkspaceResolutionTaskType {
		c.resolves++
		out, err = resolveWorkspaceTask(context.Background(), data)
	} else {
		var req workerops.ActivityInvocationRequest
		if err = json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		cell := req.GitTaskContext.CellName
		scope := ""
		if req.GitTaskContext.Workspace != nil {
			cell = req.GitTaskContext.Workspace.Cell
			scope = req.GitTaskContext.Workspace.ScopeID
		}
		env, e := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, workerops.ActivityInvocationOutput{WorkspaceScopeID: scope, OpOutput: map[string]any{"cell": cell, "scope": scope, "head": req.GitTaskContext.ResolvedBaseHash}, GitResult: contextual.GitCommitContext{ParentRef: req.GitTaskContext.BaseRef}})
		if e != nil {
			return nil, e
		}
		out, err = jobdb.NewTaskData(env)
	}
	if err != nil {
		return nil, err
	}
	c.entries = append(c.entries, workspaceReplayEntry{task: task, input: raw, out: out})
	return out, nil
}
func TestWorkspaceReplayReusesResolutionAfterSourceDisappears(t *testing.T) {
	root := t.TempDir()
	b := filepath.Join(root, "B")
	base := workspaceRepo(t, b, "B")
	workspaceTestWorker(t)
	text := fmt.Sprintf(`id: replay
version: "1"
workspace: {cell: %q}
sequence:
 - id: first
   op: workspace_probe
 - id: second
   op: workspace_probe
outputs:
 scope: "{{ sequence.second.outputs.scope }}"
 head: "{{ sequence.second.outputs.head }}"
`, b)
	rec, err := recipe.LoadRecipeFromString([]byte(text))
	require.NoError(t, err)
	job, commit := GenerateTestContext()
	stub := &workspaceReplayContext{}
	first, _, err := ExecuteRecipe(newWorkflowContext(stub), *rec, nil, job, commit)
	require.NoError(t, err)
	require.Equal(t, base, first["head"])
	require.Equal(t, 1, stub.resolves)
	require.NoError(t, os.Rename(b, b+"-removed"))
	stub.position = 0
	second, _, err := ExecuteRecipe(newWorkflowContext(stub), *rec, nil, job, commit)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, stub.resolves)
}
func TestWorkspaceValidationDoesNotAccessTarget(t *testing.T) {
	workspaceTestWorker(t)
	rec, err := recipe.LoadRecipeFromString([]byte(`id: validate
version: "1"
input_schema:
 target: {type: string, required: true}
inputs:
 target: "${{ inputs.target }}"
sequence:
 - id: inspect
   workspace: {cell: "${{ inputs.target }}"}
   op: workspace_probe
`))
	require.NoError(t, err)
	job, commit := GenerateTestContext()
	stub := &workspaceReplayContext{}
	_, _, err = ExecuteRecipe(newWorkflowContext(stub), *rec, map[string]any{"target": "https://invalid.invalid/no-access.git"}, job, commit, ExecutionOptions{Mode: ExecutionModeValidate})
	require.NoError(t, err)
	require.Zero(t, stub.resolves)
	require.Empty(t, stub.entries)
}

func (*workspaceReplayContext) SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error) {
	return jobdb.JobKey{}, fmt.Errorf("unexpected submit")
}
func (*workspaceReplayContext) SubmitRestartJob(context.Context, jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	return jobdb.JobKey{}, fmt.Errorf("unexpected restart")
}
