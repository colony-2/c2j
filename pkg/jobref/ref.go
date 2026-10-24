// Package jobref carries the identities returned by a job submission together.
package jobref

import (
	"context"
	"fmt"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Ref struct {
	TenantID string `json:"tenant_id"`
	GoalID   string `json:"goal_id"`
	JobID    string `json:"job_id"`
}

func (r Ref) Key() jobdb.JobKey { return jobdb.JobKey{TenantId: r.TenantID, JobId: r.JobID} }
func (r Ref) Validate() error {
	if r.TenantID == "" || r.GoalID == "" || r.JobID == "" {
		return fmt.Errorf("job reference requires tenant, goal, and job IDs")
	}
	return nil
}
func FromHandle(h jobdb.JobHandle) (Ref, error) {
	r := Ref{TenantID: h.JobKey.TenantId, JobID: h.JobKey.JobId, GoalID: h.GoalID}
	return r, r.Validate()
}

type Submitter interface {
	SubmitJobRef(context.Context, jobdb.SubmitJob) (Ref, error)
}
type RestartSubmitter interface {
	SubmitRestartJobRef(context.Context, jobdb.SubmitRestartJobRequest) (Ref, error)
}

// Scope is supplied by the trusted execution runtime, preserving the current
// lease's identity and submission response without an additional lookup.
type Scope struct {
	Current Ref
	Submit  func(context.Context, jobdb.SubmitJob) (Ref, error)
}
type scopeKey struct{}

func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}
func FromContext(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(scopeKey{}).(Scope)
	return s, ok
}
