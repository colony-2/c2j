package compiler

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/stretchr/testify/require"
)

func TestFreshConsultationSeesAdvancedUpstreamWithoutLosingCandidate(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "A"), filepath.Join(root, "B")
	workspaceRepo(t, a, "A")
	initial := workspaceRepo(t, b, "B")
	ws := workspaceTestWorker(t)
	job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "A"}, GitBase: contextual.GitBaseContext{BaseRepo: a, BaseRef: "main"}, CellResolution: &cellref.Context{Pattern: root + "/${{ cell }}"}}
	out, _ := runWorkspaceRecipe(t, ws, fmt.Sprintf(`id: workspace-test
version: "1"
sequence:
- id: candidate
  op: workspace_probe
  inputs: {write: candidate.txt}
- id: first
  workspace: {cell: B}
  op: workspace_probe
  inputs: {write: experiment.txt, advance: %q, absent: candidate.txt}
- id: second
  workspace: {cell: B}
  op: workspace_probe
  inputs: {read: moved, absent: experiment.txt}
- id: resume
  op: workspace_probe
  inputs: {read: candidate.txt, absent: moved}
outputs:
  first: ${{ sequence.first.outputs.head }}
  second: ${{ sequence.second.outputs.head }}
  upstream: ${{ sequence.second.outputs.text }}
  candidate: ${{ sequence.resume.outputs.text }}
`, b), job)
	current, err := common.GetCommitHash(context.Background(), b, "HEAD")
	require.NoError(t, err)
	require.NotEqual(t, initial, current)
	require.Equal(t, initial, out["first"])
	require.Equal(t, current, out["second"])
	require.Equal(t, "new tip", out["upstream"])
	require.Equal(t, "A data", out["candidate"])
}
