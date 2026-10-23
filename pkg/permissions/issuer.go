package permissions

import (
	"context"
	"fmt"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type DelegationCreator interface {
	CreateDelegation(context.Context, jobdb.ExecutionLease, jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error)
}

// Issuer is the single integration boundary for credential creation.
// Call sites do not depend on JobDB transport or attenuation details.
type Issuer interface {
	IssueRestrictedLease(context.Context, jobdb.ExecutionLease, JobDB, time.Time) (jobdb.ExecutionLease, error)
}

// JobDBIssuer uses JobDB local attenuation. Refresh is automatic in the JobDB
// client and cannot extend NotAfter or renew the owner.
type JobDBIssuer struct{ Creator DelegationCreator }

func (i JobDBIssuer) IssueRestrictedLease(ctx context.Context, parent jobdb.ExecutionLease, grant JobDB, deadline time.Time) (jobdb.ExecutionLease, error) {
	if i.Creator == nil || parent == nil {
		return nil, fmt.Errorf("delegation issuer unavailable")
	}
	if err := grant.Validate(); err != nil {
		return nil, err
	}
	if d, ok := ctx.Deadline(); ok {
		deadline = Earlier(deadline, d)
	}
	if d, ok := parent.(interface{ Delegation() *jobdb.DelegationInfo }); ok {
		if info := d.Delegation(); info != nil {
			if info.NotAfter != nil {
				deadline = Earlier(deadline, *info.NotAfter)
			}
		}
	}
	if deadline.IsZero() || !deadline.After(time.Now()) {
		return nil, fmt.Errorf("restricted lease requires a live finite task deadline")
	}
	perms := jobdb.DelegationPermissions{Secrets: append([]string(nil), grant.Secrets.Resolve...)}
	if grant.Jobs.Submit {
		perms.Submit = &jobdb.DelegationSubmitPermission{JobTypes: jobdb.DelegationNames{Any: true}, JobIDs: jobdb.DelegationNames{Any: true}}
	}
	child, err := i.Creator.CreateDelegation(ctx, parent, jobdb.CreateDelegationRequest{Permissions: perms, NotAfter: &deadline})
	if err != nil {
		return nil, fmt.Errorf("create restricted lease: %w", err)
	}
	if child == nil {
		return nil, fmt.Errorf("delegation issuer returned no lease")
	}
	return child, nil
}
