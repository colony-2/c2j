package executionruntime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (r *Runtime) restrictedAccess(tc jobworkflow.TaskContext, original *lease, grant permissions.JobDB, fresh permissions.ResolveFunc) (*permissions.Access, context.Context, context.CancelFunc, error) {
	// Keep the live TaskContext: JobDB attaches its authoritative execution
	// bounds when creating a child. Embedded execution uses in-process guards.
	ctx, cancel := context.WithCancel(tc.Context())
	var access *permissions.Access
	resolve := fresh
	submit := permissions.SubmitFunc(func(call context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
		h, err := r.submitOnCurrentLease(call, tc, original, job)
		if err != nil {
			return jobref.Ref{}, err
		}
		return jobref.FromHandle(h)
	})
	if len(grant.Secrets.Resolve) != 0 || grant.Jobs.Submit {
		if r.Issuer != nil {
			var mu sync.Mutex
			var child jobdb.ExecutionLease
			var issuedDeadline time.Time
			get := func(call context.Context) (jobdb.ExecutionLease, error) {
				mu.Lock()
				defer mu.Unlock()
				// Only the operation scope can narrow the credential. A short
				// helper/RPC call deadline controls that call's cancellation.
				d, _ := access.Context().Deadline()
				if child == nil || (!d.IsZero() && (issuedDeadline.IsZero() || d.Before(issuedDeadline))) {
					r.mu.Lock()
					owner := r.leases[tc.JobKey]
					r.mu.Unlock()
					if owner == nil || owner.leaseState != original.leaseState || tc.Context().Err() != nil {
						return nil, jobdb.ErrExecutionLeaseLost
					}
					var err error
					child, err = r.Issuer.IssueRestrictedLease(call, tc, grant, d)
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
			submit = func(call context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
				child, err := get(call)
				if err != nil {
					return jobref.Ref{}, err
				}
				handle, err := child.SubmitJob(call, jobdb.SubmitJobRequest{Job: job})
				if err != nil {
					return jobref.Ref{}, err
				}
				return jobref.FromHandle(handle)
			}
		} else if original.LeaseToken() != "" {
			cancel()
			return nil, nil, nil, fmt.Errorf("remote restricted execution requires a delegation issuer")
		}
		// Embedded runtimes are trusted in-process APIs. They use the same
		// per-call guard, but do not claim to mint or isolate a bearer credential.
	}
	access = permissions.NewAccess(ctx, grant, resolve, submit)
	return access, permissions.WithAccess(ctx, access), cancel, nil
}
