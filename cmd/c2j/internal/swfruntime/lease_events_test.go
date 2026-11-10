package swfruntime

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/localruntime"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func TestLeaseEventsThroughRuntimeWrappers(t *testing.T) {
	for _, backend := range []string{"embed", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			url := "embed:///"
			if backend == "embed" {
				t.Setenv("HOME", t.TempDir())
			} else {
				server := httptest.NewServer(celltest.Handler(remote.NewServer(toy.New())))
				t.Cleanup(server.Close)
				url = server.URL
			}
			handle, err := Open(ctx, url)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, handle.Cleanup()) })
			rt := executionruntime.New(handle.Runtime, execution.Allocation{})
			celltest.Create(t, rt, "tenant", "test", "example.test/test")
			h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "test", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: h.JobKey, WorkerID: "test-worker", Routes: []jobdb.Route{{JobType: "test"}}, LeaseDuration: time.Minute})
			require.NoError(t, err)
			require.NotNil(t, lease)
			t.Cleanup(lease.StopKeepAlive)
			service := &jobinspect.Service{Runtime: rt, Tenant: "tenant"}
			page, err := service.ListJobLeaseEvents(ctx, h.JobKey.JobId, jobinspect.LeaseEventOptions{})
			require.NoError(t, err)
			require.Len(t, page.Events, 1)
			require.Equal(t, h.JobKey, page.Events[0].JobKey)
			require.Equal(t, jobdb.LeaseEventAcquired, page.Events[0].EventType)
			require.Equal(t, "test-worker", page.Events[0].WorkerID)
		})
	}
}

func TestLeaseEventsUnsupportedThroughRuntimeWrappers(t *testing.T) {
	rt := executionruntime.New(withChapterVisibility(localruntime.New(unsupportedEventsRuntime{})), execution.Allocation{})
	service := &jobinspect.Service{Runtime: rt, Tenant: "tenant"}
	_, err := service.ListJobLeaseEvents(context.Background(), "job", jobinspect.LeaseEventOptions{})
	require.ErrorIs(t, err, jobdb.ErrLeaseEventsUnsupported)
}

// Lease-event listing is required by the upgraded WorkflowRuntime interface;
// model an unsupported implementation explicitly rather than embedding nil.
type unsupportedEventsRuntime struct{ jobdb.WorkflowRuntime }

func (unsupportedEventsRuntime) ListLeaseEvents(context.Context, jobdb.ListLeaseEventsRequest) (jobdb.ListLeaseEventsResponse, error) {
	return jobdb.ListLeaseEventsResponse{}, jobdb.ErrLeaseEventsUnsupported
}
