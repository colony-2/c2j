package workjob

import (
	"context"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type routeRecordingLease struct {
	jobdb.ExecutionLease
	request jobdb.RescheduleExecutionRequest
}

func (l *routeRecordingLease) Reschedule(_ context.Context, request jobdb.RescheduleExecutionRequest) error {
	l.request = request
	return nil
}

func TestRunOneReschedulePreservesTypedRoute(t *testing.T) {
	underlying := &routeRecordingLease{}
	runtime := &runOneRuntime{}
	lease := &runOneLease{ExecutionLease: underlying, runtime: runtime}
	request := jobdb.RescheduleExecutionRequest{
		NextRoute: jobdb.Route{JobType: " recipe:Kind ", TaskType: "input:collect:Step"},
		TaskWait:  &jobdb.TaskWait{InputOrdinal: 0, OutputOrdinal: 9, ResumeJobType: " recipe:Kind "},
	}
	require.NoError(t, lease.Reschedule(context.Background(), request))
	require.Equal(t, request, underlying.request)
	state := runtime.state()
	require.True(t, state.finalized)
	require.Equal(t, "rescheduled", state.status)
	require.Equal(t, &request.NextRoute, state.nextRoute)
	state.nextRoute.TaskType = "changed"
	require.Equal(t, &request.NextRoute, runtime.state().nextRoute)
}
