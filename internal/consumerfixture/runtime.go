// Package consumerfixture composes the retained server ports used by real
// SQLite and toy test decorators. No concrete backend is promoted.
package consumerfixture

import (
	"context"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Ports preserves backend administration and tokenized HTTP lease operations.
type Ports interface {
	KeepAliveLeaseByID(ctx context.Context, jobKey jobdb.JobKey, leaseID string, workerID string, leaseDuration time.Duration) error
	CompleteJobWithLeaseByID(ctx context.Context, jobKey jobdb.JobKey, leaseID string, workerID string, req jobdb.CompleteExecutionRequest) error
	RescheduleJobWithLeaseByID(ctx context.Context, jobKey jobdb.JobKey, leaseID string, workerID string, req jobdb.RescheduleExecutionRequest) error
	SubmitJobWithLeaseByID(ctx context.Context, parentJobKey jobdb.JobKey, leaseID string, workerID string, req jobdb.SubmitJobRequest) (jobdb.JobHandle, error)
	SubmitRestartJobWithLeaseByID(ctx context.Context, parentJobKey jobdb.JobKey, leaseID string, workerID string, req jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error)
	KeepAliveLeaseByIDWithExpiry(context.Context, jobdb.JobKey, string, string, time.Duration) (time.Time, error)

	jobdb.ConfigurationAdmin
	jobdb.JobSchemaRegistry
	jobdb.JobDeletionRuntime
	jobdb.PrimaryGoalSelectionCapability
	StartOrAppendJobWithLeaseByID(context.Context, jobdb.JobKey, string, string, jobdb.StartOrAppendJobRequest) (jobdb.JobHandle, error)
	RenewExecutionLeaseByID(context.Context, jobdb.JobKey, string, string, time.Duration) (jobdb.RenewableExecutionLease, error)
	ValidateExecutionLeaseByID(context.Context, jobdb.JobKey, string, string) (jobdb.LeaseValidity, error)
	ConfigurationLimits() jobdb.ConfigurationLimits
	SecretResolutionLimits() jobdb.SecretLimits
	ResolveCellWithLeaseByID(context.Context, jobdb.JobKey, string, string, string) (jobdb.ResolvedCell, error)
	LookupCellWithLeaseByID(context.Context, jobdb.JobKey, string, string, string) (jobdb.ResolvedCell, error)
	CreateCellWithLeaseByID(context.Context, jobdb.JobKey, string, string, jobdb.CreateCellRequest) (jobdb.Cell, error)
}

// DevSecretAdmin retains development-store administration where the backend
// supplies it. It is separate because the toy backend does not have this port.
type DevSecretAdmin interface {
	SetDevSecret(context.Context, string, string, string) error
	RemoveDevSecret(context.Context, string, string) error
}

// Runtime retains execution and supported server capabilities without attachment.
type Runtime struct {
	jobdb.ConsumerRuntime
	Ports
	DevSecretAdmin
}

// New requires genuine optional implementations; it never synthesizes authority.
func New(rt interface {
	jobdb.ConsumerRuntime
	Ports
	DevSecretAdmin
}) Runtime {
	return Runtime{ConsumerRuntime: rt, Ports: rt, DevSecretAdmin: rt}
}
