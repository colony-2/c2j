package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/contextual"
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

type workspaceSubmitProbeInput struct {
	Mode string `json:"mode"`
}
type workspaceSubmitProbeOutput struct {
	JobID string `json:"job_id"`
}

func TestWorkerBrokerSubmitsIncomingAndLiveWorkspace(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		for _, mode := range []string{"start", "now"} {
			t.Run(fmt.Sprintf("%s/scoped=%t", mode, scoped), func(t *testing.T) {
				root := t.TempDir()
				workspaceRepo(t, root, "A")
				other := filepath.Join(t.TempDir(), "B")
				workspaceRepo(t, other, "B")
				op := coreops.NewActivityMappedOpV2[workspaceSubmitProbeInput, workspaceSubmitProbeOutput](coreops.OpMetadata{Type: "workspace_submit_probe"}, func(deps coreops.OpDependencies, ctx context.Context, in workspaceSubmitProbeInput) (workspaceSubmitProbeOutput, error) {
					if err := os.WriteFile(filepath.Join(deps.WorktreePath(), "candidate"), []byte("during task"), 0644); err != nil {
						return workspaceSubmitProbeOutput{}, err
					}
					child, err := recipe.LoadRecipeFromString([]byte(`id: workspace_child
version: "1"
sequence:
 - id: read
   op: workspace_probe
   inputs: {read: candidate}
outputs:
 text: "{{ sequence.read.outputs.text }}"
`))
					if err != nil {
						return workspaceSubmitProbeOutput{}, err
					}
					req, err := childbroker.NewSubmitRequest(ctx, workflowctl.StartJob{TenantId: deps.JobTool().GetJobKey().TenantId, RecipeName: "workspace_child", Cell: "test", CellRepo: celltest.Repo("")}, nil, *child)
					if err != nil {
						return workspaceSubmitProbeOutput{}, err
					}
					req.WorkspaceMode = in.Mode
					resp, err := childbroker.SubmitLocal(ctx, req)
					return workspaceSubmitProbeOutput{JobID: resp.JobID}, err
				})
				workspaceTestWorker(t, op)
				eng := newToyEngine(t, "workspace-tests", nil, map[string]string{"A": root, "B": other})
				ctl := &workerworkflow.SWFWorkflowControl{Engine: eng}
				registry, err := workerops.NewActivityRegistry()
				require.NoError(t, err)
				ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().WithWorkflowControl(ctl).Build(), registry)
				require.NoError(t, err)
				require.NoError(t, eng.RegisterWorkers(ws))
				yaml := fmt.Sprintf(`id: workspace-test
version: "1"
sequence:
 - id: previous_task
   op: workspace_probe
   inputs: {write: candidate}
 - id: submit
   op: workspace_submit_probe
   inputs: {mode: %s}
 - id: await
   op: recipe.await_result_soft
   inputs: {target: {job: "{{ sequence.submit.outputs.job_id }}"}}
outputs:
 result: "${{ sequence.await.outputs }}"
`, mode)
				if scoped {
					yaml += fmt.Sprintf("workspace: {cell: B, repo: %q}\n", celltest.Repo(other))
				}
				rec, err := recipe.LoadRecipeFromString([]byte(yaml))
				require.NoError(t, err)
				celltest.Create(t, eng, "workspace-tests", "A", celltest.Repo(root))
				ref, err := starter.StartRecipeJob(celltest.Context(context.Background()), workflowctl.StartJob{TenantId: "workspace-tests", RecipeName: "workspace-test", GitRef: "main", JobContext: contextual.JobContext{CellResolution: &cellref.Context{}, Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: root, BaseRef: "main"}}, Cell: "A", CellRepo: celltest.Repo(root)}, eng, *rec)
				require.NoError(t, err)
				require.NoError(t, jobworkflow.WaitForJobToComplete(celltest.Context(context.Background()), 20*time.Second, ref.Key(), eng))
				data, err := swfutil.JobResult(celltest.Context(context.Background()), eng, ref.Key())
				require.NoError(t, err)
				raw, err := data.GetData()
				require.NoError(t, err)
				var out map[string]any
				require.NoError(t, json.Unmarshal(raw, &out))
				result := out["result"].(map[string]any)
				require.Equal(t, "completed", result["status"], "%#v", result)
				want := "A data"
				if scoped {
					want = "B data"
				}
				if mode == "now" {
					want = "during task"
				}
				require.Equal(t, want, result["outputs"].(map[string]any)["text"])
			})
		}
	}

}
