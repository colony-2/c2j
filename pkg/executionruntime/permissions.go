package executionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (r *Runtime) restrictedAccess(parent context.Context, tc jobworkflow.TaskContext, original *lease, grant permissions.JobDB, fresh permissions.ResolveFunc, policies ...permissions.State) (*permissions.Access, context.Context, context.CancelFunc, error) {
	// Keep the live TaskContext: JobDB attaches its authoritative execution
	// bounds when creating a child. Embedded execution uses in-process guards.
	ctx, cancel := context.WithCancel(parent)
	var access *permissions.Access
	resolve := fresh
	var hookPolicy json.RawMessage
	if len(policies) > 0 {
		hookPolicy = policies[0].ChildPolicy
	}
	submit := permissions.SubmitFunc(func(call context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
		if len(hookPolicy) > 0 {
			var err error
			childGrant := grant
			if policies[0].ChildGrant != nil {
				childGrant = hookpolicy.Intersect(grant, *policies[0].ChildGrant)
			}
			job, err = constrainHookChild(job, childGrant, hookPolicy)
			if err != nil {
				return jobref.Ref{}, err
			}
		}
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
					delegatedGrant := grant
					// Hook child jobs must go through the controller's submission
					// path, which installs the ceiling. A raw token cannot bypass it.
					if len(hookPolicy) > 0 {
						delegatedGrant.Jobs.Submit = false
					}
					child, err = r.Issuer.IssueRestrictedLease(call, tc, delegatedGrant, d)
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
			if len(hookPolicy) == 0 {
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
			}
		} else if original.LeaseToken() != "" {
			cancel()
			return nil, nil, nil, fmt.Errorf("remote restricted execution requires a delegation issuer")
		}
		// Embedded runtimes are trusted in-process APIs. They use the same
		// per-call guard, but do not claim to mint or isolate a bearer credential.
	}
	if redactor := secrets.RedactorFromContext(parent); redactor != nil && resolve != nil {
		inner := resolve
		resolve = func(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
			result, err := inner(ctx, names)
			if err != nil {
				return jobdb.SecretResolveResult{}, err
			}
			for _, name := range names {
				if value, ok := result.Secrets[name]; ok {
					if err := redactor.Register(value.Value); err != nil {
						return jobdb.SecretResolveResult{}, err
					}
				}
			}
			return result, nil
		}
	}
	access = permissions.NewAccess(ctx, grant, resolve, submit)
	return access, permissions.WithAccess(ctx, access), cancel, nil
}

func constrainHookChild(job jobdb.SubmitJob, grant permissions.JobDB, policy json.RawMessage) (jobdb.SubmitJob, error) {
	if job.JobType != "recipe" {
		return job, fmt.Errorf("hook child submission requires a recipe job that enforces inherited permissions")
	}
	if job.Data == nil {
		return job, fmt.Errorf("hook child job data is required")
	}
	raw, err := job.Data.GetData()
	if err != nil {
		return job, err
	}
	var start workflowctl.StartJob
	if err = json.Unmarshal(raw, &start); err != nil {
		return job, err
	}
	// Replace any claimed lineage authority, rather than merging untrusted input.
	start.JobContext.InheritedPermissions = &permissions.State{Version: permissions.Version, Grant: &grant, Limit: &grant, Inputs: &permissions.InputPolicy{Foreign: true, Allowed: append([]string(nil), grant.Secrets.Resolve...)}, ChildPolicy: append(json.RawMessage(nil), policy...)}
	arts, err := job.Data.GetArtifacts()
	if err != nil {
		return job, err
	}
	job.Data, err = jobdb.NewTaskData(start, arts...)
	return job, err
}
