package ops

import (
	"context"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/pkg/gitprofile"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestRestrictedInputsDeniedBeforeInstallationOrResolution(t *testing.T) {
	type input struct {
		Token string `json:"token" secret:"true"`
	}
	worker := taskWorker{sessions: secrets.NewSessions(), reg: ActivityRegistration{Step: coreops.TaskStep{InputType: reflect.TypeFor[input]()}}}
	tc := jobworkflow.TaskContext{JobKey: jobdb.JobKey{TenantId: "test", JobId: "denied"}, Step: 1}
	state := permissions.State{Version: permissions.Version, Grant: &permissions.JobDB{}}
	request := ActivityInvocationRequest{
		Permissions: &state,
		Input:       map[string]any{"token": secrets.Reference{Name: "rw"}},
		// If installation runs before admission, it fails with a different error.
		Preparation: PreparationRequest{Scopes: []toolenv.Scope{{ID: "test", Packages: []string{"unsupported:tool"}}}},
	}
	_, err := worker.Run(tc, jobdb.NewTaskDataOrPanic(request))
	require.ErrorContains(t, err, "requires executionruntime.WrapTaskWorker")
	access := permissions.NewAccess(context.Background(), permissions.JobDB{}, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
		t.Fatal("denied input reached resolver")
		return jobdb.SecretResolveResult{}, nil
	}, nil)
	ctx := permissions.WithAccess(context.Background(), access)
	scope, err := gitprofile.NewScope(ctx, gitprofile.Selection{}, jobdb.ConfigurationSnapshot{}, access.Resolve, "")
	require.NoError(t, err)
	defer scope.Close()
	unbind := gitprofile.BindTask(tc, scope)
	defer unbind()
	_, err = worker.Run(tc, jobdb.NewTaskDataOrPanic(request))
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
}
