package ops

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceInspectionRecordsObservationWithoutPinning(t *testing.T) {
	for _, constant := range []bool{false, true} {
		t.Run(map[bool]string{true: "const", false: "no-change"}[constant], func(t *testing.T) {
			repo, base, cleanup := setupGitRepo(t)
			defer cleanup()
			_, err := common.ExecuteGitCommand(context.Background(), repo, "branch", "-M", "main")
			require.NoError(t, err)
			reg := ActivityRegistration{Step: recipeops.TaskStep{Invoke: func(recipeops.OpDependencies, context.Context, map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{"ok": true}, nil
			}}}
			exec := opExecutor{deps: recipeops.NewServiceDepsBuilder().Build(), reg: reg, controller: gitstate.NewController(nil)}
			out, arts, e := exec.do(celltest.Context(context.Background()), &jt{jobdb.JobKey{TenantId: "test", JobId: "inspect"}}, ActivityInvocationRequest{Input: map[string]interface{}{}, Const: constant, GitTaskContext: gitstate.GlobalGitTaskContext{BaseRepo: repo, BaseRef: "main", CellName: "test"}}, nil)
			require.NoError(t, e)
			require.Empty(t, arts)
			require.Empty(t, out.GitResult.PersistHash)
			require.Equal(t, "main", out.GitResult.ParentRef)
			require.NotNil(t, out.WorkspaceBefore)
			require.NotNil(t, out.WorkspaceAfter)
			require.Equal(t, base, out.WorkspaceBefore.HeadCommit)
			require.Equal(t, base, out.WorkspaceAfter.HeadCommit)
			require.Equal(t, base, out.WorkspaceAfter.BaseCommit)
		})
	}
}
