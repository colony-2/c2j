package permissions

import (
	"context"
	"fmt"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// TaskDelegator is the live task's engine-owned delegation capability. A copy of
// workflow.TaskContext preserves it; a reconstructed context does not.
type TaskDelegator interface {
	CreateDelegation(context.Context, jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error)
}

// Issuer is the single integration boundary for credential creation.
// The deadline is an optional operation limit; JobDB supplies execution bounds.
type Issuer interface {
	IssueRestrictedLease(context.Context, TaskDelegator, JobDB, time.Time) (jobdb.ExecutionLease, error)
}

// JobDBIssuer uses task-scoped local attenuation. The caller context controls
// cancellation only; its request timeout must not become a credential deadline.
type JobDBIssuer struct{}

func (i JobDBIssuer) IssueRestrictedLease(ctx context.Context, task TaskDelegator, grant JobDB, deadline time.Time) (jobdb.ExecutionLease, error) {
	if task == nil {
		return nil, jobdb.ErrLeaseDelegationUnsupported
	}
	if err := grant.Validate(); err != nil {
		return nil, err
	}
	perms := jobdb.DelegationPermissions{Secrets: append([]string(nil), grant.Secrets.Resolve...)}
	if grant.Jobs.Submit {
		perms.Submit = &jobdb.DelegationSubmitPermission{JobTypes: jobdb.DelegationNames{Any: true}, JobIDs: jobdb.DelegationNames{Any: true}}
	}
	req := jobdb.CreateDelegationRequest{Permissions: perms}
	if !deadline.IsZero() {
		req.NotAfter = &deadline
	}
	child, err := task.CreateDelegation(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create restricted lease: %w", err)
	}
	if child == nil {
		return nil, fmt.Errorf("delegation issuer returned no lease")
	}
	// JobDB supplies all applicable bounds. Without a hard deadline the child
	// remains restricted by its permissions and the exact parent lease's lifetime.
	info, ok := child.(interface{ Delegation() *jobdb.DelegationInfo })
	if !ok {
		return nil, fmt.Errorf("restricted lease is missing delegation metadata")
	}
	bounds := info.Delegation()
	if bounds == nil {
		return nil, fmt.Errorf("restricted lease is missing delegation metadata")
	}
	if bounds.NotAfter != nil && !bounds.NotAfter.After(time.Now()) {
		return nil, fmt.Errorf("restricted lease deadline has expired")
	}
	return child, nil
}
