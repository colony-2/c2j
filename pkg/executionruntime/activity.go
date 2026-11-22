package executionruntime

import (
	"context"
	"time"

	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ActivitySnapshot contains only local probe data, never execution authority.
type ActivitySnapshot struct {
	TaskType             string     `json:"task_type"`
	ExtensionOp          *string    `json:"extension_op"`
	CurrentTaskStartedAt time.Time  `json:"current_task_started_at"`
	LeaseExpiresAt       *time.Time `json:"lease_expires_at"`
}
type runActivity struct {
	frameworkAt time.Time
	sequence    uint64
	active      map[uint64]ActivitySnapshot
	now         func() time.Time
}

// EnableActivity enables optional live operation tracking for one CLI invocation.
// Call it before starting execution. Library callers need not enable it.
func (r *Runtime) EnableActivity(started time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activity = &runActivity{frameworkAt: started.UTC(), active: map[uint64]ActivitySnapshot{}, now: time.Now}
}

func (r *Runtime) ActivitySnapshot(key jobdb.JobKey) ActivitySnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.activity
	s := ActivitySnapshot{TaskType: "framework"}
	if a != nil {
		s.CurrentTaskStartedAt = a.frameworkAt
		var newest uint64
		for token, active := range a.active {
			if token > newest {
				newest, s = token, active
			}
		}
	}
	if l := r.leases[key]; l != nil {
		if expiry, ok := l.ExecutionLease.(interface{ LeaseExpiry() time.Time }); ok {
			if at := expiry.LeaseExpiry(); !at.IsZero() {
				at = at.UTC()
				s.LeaseExpiresAt = &at
			}
		}
	}
	if s.ExtensionOp != nil {
		copy := *s.ExtensionOp
		s.ExtensionOp = &copy
	}
	return s
}

func (r *Runtime) clearActivityLocked() {
	if a := r.activity; a != nil && len(a.active) > 0 {
		clear(a.active)
		a.frameworkAt = a.now().UTC()
	}
}

func (r *Runtime) activityContext(ctx context.Context, key jobdb.JobKey, state *leaseState) context.Context {
	return task.WithActivityReporter(ctx, func(kind string) (func(string), func()) {
		r.mu.Lock()
		a := r.activity
		current := r.leases[key]
		if a == nil || current == nil || current.leaseState != state {
			r.mu.Unlock()
			return func(string) {}, func() {}
		}
		a.sequence++
		token := a.sequence
		a.active[token] = ActivitySnapshot{TaskType: kind, CurrentTaskStartedAt: a.now().UTC()}
		r.mu.Unlock()
		valid := func() bool {
			l := r.leases[key]
			_, exists := a.active[token]
			return r.activity == a && l != nil && l.leaseState == state && exists
		}
		return func(selector string) {
				r.mu.Lock()
				defer r.mu.Unlock()
				if valid() {
					s := a.active[token]
					s.ExtensionOp = &selector
					a.active[token] = s
				}
			}, func() {
				r.mu.Lock()
				defer r.mu.Unlock()
				if valid() {
					delete(a.active, token)
					if len(a.active) == 0 {
						a.frameworkAt = a.now().UTC()
					}
				}
			}
	})
}
