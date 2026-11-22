package executionruntime

import (
	"context"
	"time"

	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (l *lease) notifyHistoryLocked() {
	if l.historyChanged != nil {
		close(l.historyChanged)
		l.historyChanged = nil
	}
}

// bindHistory watches the current lease, not the original expiry snapshot.
// Renewal wakes the timer; replacement, forget and failed renewal revoke reads.
func (r *Runtime) bindHistory(call context.Context, access *permissions.Access, tc jobworkflow.TaskContext, owner *lease) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(call)
	stopTask := context.AfterFunc(tc.Context(), func() { cancel(context.Cause(tc.Context())) })
	stopScope := context.AfterFunc(access.Context(), func() { cancel(context.Cause(access.Context())) })
	go func() {
		for {
			r.mu.Lock()
			live := r.leases[tc.JobKey]
			if live == nil || live.leaseState != owner.leaseState {
				r.mu.Unlock()
				cancel(jobdb.ErrExecutionLeaseLost)
				return
			}
			if live.historyChanged == nil {
				live.historyChanged = make(chan struct{})
			}
			changed := live.historyChanged
			var expires time.Time
			if expiry, ok := live.ExecutionLease.(interface{ LeaseExpiry() time.Time }); ok {
				expires = expiry.LeaseExpiry()
			}
			r.mu.Unlock()
			var timer *time.Timer
			var tick <-chan time.Time
			if !expires.IsZero() {
				timer = time.NewTimer(time.Until(expires))
				tick = timer.C
			}
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-changed:
				if timer != nil {
					timer.Stop()
				}
				continue
			case <-tick:
				// A renewal can win concurrently with the old timer. Inspect again.
				r.mu.Lock()
				current := r.leases[tc.JobKey]
				expired := current == nil || current.leaseState != owner.leaseState
				if !expired {
					if expiry, ok := current.ExecutionLease.(interface{ LeaseExpiry() time.Time }); ok {
						expired = !expiry.LeaseExpiry().After(time.Now())
					}
				}
				r.mu.Unlock()
				if expired {
					cancel(jobdb.ErrExecutionLeaseLost)
					return
				}
			}
		}
	}()
	return ctx, func() { stopTask(); stopScope(); cancel(context.Canceled) }
}
