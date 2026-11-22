package executionruntime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestHealthActivityTransitions(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	rt := New(nil, execution.Allocation{})
	at := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
	rt.EnableActivity(at)
	rt.activity.now = func() time.Time { return at }
	snapshot := func() ActivitySnapshot { return rt.ActivitySnapshot(key) }
	require.Equal(t, "framework", snapshot().TaskType)
	require.Nil(t, snapshot().LeaseExpiresAt)
	owner := rt.wrapLease(&selfLease{key: key, expiry: at.Add(time.Minute)}, &leaseState{}).(*lease)
	local := rt.activityContext(context.Background(), key, owner.leaseState)
	at = at.Add(time.Second)
	update, finish := task.BeginActivity(local, "first:step")
	firstStart := at
	update("./ops/check")
	require.Equal(t, "./ops/check", *snapshot().ExtensionOp)
	at = at.Add(time.Second)
	_, finishNew := task.BeginActivity(local, "second:step")
	require.Equal(t, "second:step", snapshot().TaskType)
	rt.wrapLease(&selfLease{key: key, expiry: at.Add(2 * time.Minute)}, owner.leaseState)
	require.Equal(t, at, snapshot().CurrentTaskStartedAt)
	require.Equal(t, at.Add(2*time.Minute), *snapshot().LeaseExpiresAt)
	finishNew()
	require.Equal(t, "first:step", snapshot().TaskType)
	require.Equal(t, firstStart, snapshot().CurrentTaskStartedAt)
	at = at.Add(time.Second)
	finish()
	finish()
	require.Equal(t, "framework", snapshot().TaskType)
	require.Equal(t, at, snapshot().CurrentTaskStartedAt)
	frameworkStart := at
	at = at.Add(time.Second)
	require.Equal(t, frameworkStart, snapshot().CurrentTaskStartedAt)
	_, finishOld := task.BeginActivity(local, "old")
	owner.forget()
	require.Nil(t, snapshot().LeaseExpiresAt)
	replacement := rt.wrapLease(&selfLease{key: key}, &leaseState{}).(*lease)
	replacementLocal := rt.activityContext(context.Background(), key, replacement.leaseState)
	_, finishReplacement := task.BeginActivity(replacementLocal, "replacement")
	update("stale")
	finishOld()
	owner.forget()
	require.Equal(t, "replacement", snapshot().TaskType)
	require.Nil(t, snapshot().ExtensionOp)
	require.Nil(t, snapshot().LeaseExpiresAt)
	finishReplacement()
}

func TestHealthActivityPastExpiryAndConcurrentCallbacks(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	rt := New(nil, execution.Allocation{})
	rt.EnableActivity(time.Now())
	expired := time.Now().Add(-time.Minute)
	original := rt.wrapLease(&selfLease{key: key, expiry: expired}, &leaseState{}).(*lease)
	require.Equal(t, expired.UTC(), *rt.ActivitySnapshot(key).LeaseExpiresAt)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				rt.mu.Lock()
				owner := rt.leases[key]
				rt.mu.Unlock()
				if owner == nil {
					continue
				}
				local := rt.activityContext(context.Background(), key, owner.leaseState)
				update, finish := task.BeginActivity(local, "op:step")
				update("./op")
				s := rt.ActivitySnapshot(key)
				require.False(t, s.CurrentTaskStartedAt.IsZero())
				finish()
				finish()
			}
		})
	}
	wg.Go(func() {
		for j := 0; j < 100; j++ {
			original.forget()
			rt.wrapLease(&selfLease{key: key, expiry: expired}, &leaseState{})
		}
	})
	wg.Wait()
}

// These fake leases use the same removal paths as the heartbeat/runner.
type healthLease struct {
	*selfLease
	renew func() (jobdb.RenewableExecutionLease, error)
}

func (l *healthLease) Renew(context.Context) (jobdb.RenewableExecutionLease, error)     { return l.renew() }
func (*healthLease) Complete(context.Context, jobdb.CompleteExecutionRequest) error     { return nil }
func (*healthLease) Reschedule(context.Context, jobdb.RescheduleExecutionRequest) error { return nil }
func (*healthLease) StopKeepAlive()                                                     {}
func (*healthLease) ClientPayload() json.RawMessage                                     { return json.RawMessage(`{}`) }
func (*healthLease) ClientPayloadRevision() int64                                       { return 0 }

func TestHealthLeaseRemovalAndLateRenewal(t *testing.T) {
	key := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	for _, removal := range []string{"complete", "reschedule", "renewal failure", "stop"} {
		t.Run(removal, func(t *testing.T) {
			rt := New(nil, execution.Allocation{})
			rt.EnableActivity(time.Now())
			backing := &healthLease{selfLease: &selfLease{key: key, expiry: time.Now().Add(time.Minute)}, renew: func() (jobdb.RenewableExecutionLease, error) { return nil, jobdb.ErrExecutionLeaseLost }}
			owner := rt.wrapLease(backing, &leaseState{}).(*renewableLease)
			local := rt.activityContext(context.Background(), key, owner.leaseState)
			_, finish := task.BeginActivity(local, "op:step")
			switch removal {
			case "complete":
				require.NoError(t, owner.Complete(context.Background(), jobdb.CompleteExecutionRequest{}))
			case "reschedule":
				require.NoError(t, owner.Reschedule(context.Background(), jobdb.RescheduleExecutionRequest{}))
			case "renewal failure":
				_, err := owner.Renew(context.Background())
				require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
			case "stop":
				owner.StopKeepAlive()
			}
			s := rt.ActivitySnapshot(key)
			require.Equal(t, "framework", s.TaskType)
			require.Nil(t, s.LeaseExpiresAt)
			finish()
		})
	}
	rt := New(nil, execution.Allocation{})
	rt.EnableActivity(time.Now())
	entered, release := make(chan struct{}), make(chan struct{})
	backing := &healthLease{selfLease: &selfLease{key: key, expiry: time.Now()}}
	backing.renew = func() (jobdb.RenewableExecutionLease, error) {
		close(entered)
		<-release
		return &healthLease{selfLease: &selfLease{key: key, expiry: time.Now().Add(time.Hour)}}, nil
	}
	owner := rt.wrapLease(backing, &leaseState{}).(*renewableLease)
	done := make(chan error, 1)
	go func() { _, err := owner.Renew(context.Background()); done <- err }()
	<-entered
	owner.forget()
	replacement := rt.wrapLease(&selfLease{key: key}, &leaseState{}).(*lease)
	_, finish := task.BeginActivity(rt.activityContext(context.Background(), key, replacement.leaseState), "replacement")
	close(release)
	require.ErrorIs(t, <-done, jobdb.ErrExecutionLeaseLost)
	require.Equal(t, "replacement", rt.ActivitySnapshot(key).TaskType)
	require.Nil(t, rt.ActivitySnapshot(key).LeaseExpiresAt)
	finish()
}

func (*healthLease) LeaseWorkerID() string { return "dispatcher" }
