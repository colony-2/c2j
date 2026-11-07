package recipejob

import (
	"context"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// CrashConcernAdmin is the operator recovery surface implemented by JobDB
// runtimes and jobdbschema.WorkflowEngine. It is separate from task submission
// and inspection authority.
type CrashConcernAdmin interface {
	ClearCrashConcern(context.Context, jobdb.ClearCrashConcernRequest) error
}

// ClearCrashConcern resets the consecutive expired-lease count on the existing
// job. History and scheduling constraints are preserved; no lease is acquired.
// The caller must have tenant lifecycle authority, as for cancellation.
// JobDB's validation, not-found, conflict, and authorization errors pass through.
func ClearCrashConcern(ctx context.Context, admin CrashConcernAdmin, req jobdb.ClearCrashConcernRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	return admin.ClearCrashConcern(ctx, req)
}
