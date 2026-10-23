package executionruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// Deadline must come from the engine, including enclosing scopes and attempts.
// Pending execution records are not necessarily durable at dispatch; history
// scans and a fresh now+timeout calculation cannot recover this value safely.
func (r *Runtime) taskDeadline(tc jobworkflow.TaskContext) (time.Time, error) {
	// Require the engine accessor. An ordinary caller context deadline alone
	// can be later than JobDB's privately enforced node/scope deadline.
	if engine, ok := any(tc).(interface{ Deadline() (time.Time, bool) }); ok {
		if deadline, bounded := engine.Deadline(); bounded && !deadline.IsZero() {
			return deadline, nil
		}
	}
	return time.Time{}, fmt.Errorf("restricted delegation requires JobDB to expose the effective task deadline")
}

func (r *Runtime) restrictedAccess(tc jobworkflow.TaskContext, original *lease, grant permissions.JobDB, fresh permissions.ResolveFunc) (*permissions.Access, context.Context, context.CancelFunc, error) {
	// Embedded execution has no bearer token: the engine's cancellation is
	// sufficient to bound its in-process guard. Remote issuance requires the
	// absolute deadline, never a timeout restarted at minting.
	ctx, cancel := context.WithCancel(tc.Context())
	resolve := fresh
	submit := permissions.SubmitFunc(tc.SubmitJob)
	if len(grant.Secrets.Resolve) != 0 || grant.Jobs.Submit {
		if r.Issuer != nil {
			deadline, err := r.taskDeadline(tc)
			if err != nil {
				cancel()
				return nil, nil, nil, err
			}
			var mu sync.Mutex
			var child jobdb.ExecutionLease
			var issuedDeadline time.Time
			get := func(call context.Context) (jobdb.ExecutionLease, error) {
				mu.Lock()
				defer mu.Unlock()
				d := deadline
				if callDeadline, ok := call.Deadline(); ok {
					d = permissions.Earlier(d, callDeadline)
				}
				if child == nil || d.Before(issuedDeadline) || !issuedDeadline.After(time.Now()) {
					r.mu.Lock()
					owner := r.leases[tc.JobKey]
					r.mu.Unlock()
					if owner == nil || owner.leaseState != original.leaseState || tc.Context().Err() != nil {
						return nil, jobdb.ErrExecutionLeaseLost
					}
					var err error
					child, err = r.Issuer.IssueRestrictedLease(call, owner.ExecutionLease, grant, d)
					if err != nil {
						return nil, err
					}
					issuedDeadline = d
				}
				return child, nil
			}
			resolve = func(call context.Context, names []string) (jobdb.SecretResolveResult, error) {
				child, err := get(call)
				if err != nil {
					return jobdb.SecretResolveResult{}, err
				}
				return child.ResolveSecrets(call, names)
			}
			submit = func(call context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
				child, err := get(call)
				if err != nil {
					return jobdb.JobKey{}, err
				}
				handle, err := child.SubmitJob(call, jobdb.SubmitJobRequest{Job: job})
				return handle.JobKey, err
			}
		} else if original.LeaseToken() != "" {
			cancel()
			return nil, nil, nil, fmt.Errorf("remote restricted execution requires a delegation issuer")
		}
		// Embedded runtimes are trusted in-process APIs. They use the same
		// per-call guard, but do not claim to mint or isolate a bearer credential.
	}
	access := permissions.NewAccess(ctx, grant, resolve, submit)
	return access, permissions.WithAccess(ctx, access), cancel, nil
}
