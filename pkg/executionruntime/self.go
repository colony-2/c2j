package executionruntime

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/colony-2/c2j/pkg/hookpolicy"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// selfProvider only reads already-held authority. In particular, it must never
// call the lazy delegation issuer, a secret resolver, or lease renewal.
func (r *Runtime) selfProvider(tc jobworkflow.TaskContext, original *lease, access *permissions.Access, policies []permissions.State, delegation func() *jobdb.DelegationInfo) selfinfo.Provider {
	return func() selfinfo.Info {
		i := selfinfo.Unknown("runtime_metadata_unavailable")
		i.Execution.Status = "present"
		i.Execution.Source = "runtime_helper"
		i.Execution.ReasonCode = ""
		i.Execution.TenantID = selfinfo.String(tc.JobKey.TenantId)
		i.Execution.JobID = selfinfo.String(tc.JobKey.JobId)
		i.Execution.CellName = selfinfo.String(tc.Job().Cell)
		i.Execution.CellID = selfinfo.String(tc.Job().CellID)
		i.Execution.CellRepo = selfinfo.String(tc.Job().CellRepo)
		i.Execution.TaskID = selfinfo.String(tc.InvocationID)
		if tc.AttemptNumber > 0 {
			n := tc.AttemptNumber
			i.Execution.AttemptNumber = &n
		}
		if tc.Step > 0 {
			n := tc.Step
			i.Execution.ResultOrdinal = &n
		}
		grant := access.Grant()
		// An imported restricted parent can be narrower than the recipe policy.
		if bounds := original.Delegation(); bounds != nil {
			grant.Jobs.Submit = grant.Jobs.Submit && bounds.Permissions.Submit != nil
			grant.Secrets.Resolve = slices.DeleteFunc(grant.Secrets.Resolve, func(n string) bool { return !slices.Contains(bounds.Permissions.Secrets, n) })
			ceiling := permissions.JobDB{}
			if names := bounds.Permissions.CreateCells; names != nil {
				ceiling.Cells.Create = names.Any || len(names.Names) > 0
				if !names.Any {
					ceiling.Cells.CellNames = slices.Clone(names.Names)
				}
			}
			grant.Cells = permissions.Intersect(grant, ceiling).Cells
		}
		if grant.Secrets.Resolve == nil {
			grant.Secrets.Resolve = []string{}
		}
		i.Permissions = selfinfo.Permissions{Status: "known", Scope: "current_execution", Source: "effective_operation_access", ParentBounds: original.Delegation(), JobDB: &grant, ChildCeiling: selfinfo.Ceiling{Status: "unknown"}}
		if len(policies) > 0 {
			i.Permissions.InheritedDenials = policies[0].InheritedDenials()
		}
		if len(policies) > 0 && len(policies[0].ChildPolicy) > 0 {
			ceiling := grant
			if policies[0].ChildGrant != nil {
				ceiling = hookpolicy.Intersect(grant, *policies[0].ChildGrant)
			}
			i.Permissions.ChildCeiling = selfinfo.Ceiling{Status: "known", JobDB: &ceiling, JobTypes: []string{"recipe"}}
		}
		l := selfinfo.Lease{Status: "active", AuthorityMode: "controller_mediated", LeaseID: selfinfo.String(original.LeaseID()), Validation: "last_known", RenewalOwner: selfinfo.String("worker"), DelegationStatus: "not_issued"}
		if original.LeaseToken() == "" {
			l.AuthorityMode = "embedded_guard"
		}
		route := original.Route()
		l.Route = &route
		scope := access.Context()
		if deadline, ok := scope.Deadline(); ok {
			l.OperationDeadline = selfinfo.Time(deadline)
		}
		r.mu.Lock()
		current := r.leases[tc.JobKey]
		r.mu.Unlock()
		if current == nil || current.leaseState != original.leaseState {
			l.Status = "lost"
			l.ReasonCode = "execution_lease_lost"
		} else if renewable, ok := current.ExecutionLease.(interface{ LeaseExpiry() time.Time }); ok {
			l.ExpiresAt = selfinfo.Time(renewable.LeaseExpiry())
			if l.ExpiresAt != nil && !l.ExpiresAt.After(i.ObservedAt) {
				l.Status = "expired"
				l.ReasonCode = "last_known_lease_expired"
			}
		}
		if scope.Err() != nil {
			l.Status = "cancelled"
			l.ReasonCode = "execution_cancelled"
			var timeout *jobdb.TimeoutError
			if errors.Is(context.Cause(scope), context.DeadlineExceeded) || errors.As(context.Cause(scope), &timeout) {
				l.Status = "expired"
				l.ReasonCode = "operation_deadline_exceeded"
			}
			if errors.Is(context.Cause(scope), jobdb.ErrExecutionLeaseLost) {
				l.Status = "lost"
				l.ReasonCode = "execution_lease_lost"
			}
		}
		l.Delegation = delegation()
		if l.Delegation != nil {
			l.DelegationStatus = "issued"
		}
		i.Lease = l
		return i
	}
}
