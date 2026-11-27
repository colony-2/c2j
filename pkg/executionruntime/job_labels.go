package executionruntime

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/colony-2/c2j/internal/joblabelscope"
	"github.com/colony-2/c2j/pkg/joblabel"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

type jobLabelPolicy struct {
	job       jobdb.JobKey
	node      string
	extension string
}

// ConfigureJobLabelEdits installs a trusted startup grant for exactly one node
// of one resolved job. An empty selector authorizes only the built-in op.
func (r *Runtime) ConfigureJobLabelEdits(job jobdb.JobKey, node, pinnedSelector string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobLabels = jobLabelPolicy{}
	if job.Validate() != nil || node == "" || node == "." || node == ".." || strings.HasPrefix(node, "../") || path.IsAbs(node) || path.Clean(node) != node || strings.ContainsRune(node, '\\') || strings.ContainsFunc(node, func(char rune) bool { return unicode.IsControl(char) || unicode.IsSpace(char) }) {
		return fmt.Errorf("job label grant requires a resolved job and canonical node path")
	}
	if pinnedSelector != "" && !extensions.IsPinnedGitSelector(pinnedSelector) {
		return fmt.Errorf("job label extension grant requires a canonical Git selector with a full commit")
	}
	r.jobLabels = jobLabelPolicy{job: job, node: node, extension: pinnedSelector}
	return nil
}

func (r *Runtime) bindJobLabelEdit(ctx context.Context, task jobworkflow.TaskContext, original *lease, node, taskType string, policy permissions.State) context.Context {
	r.mu.Lock()
	grant := r.jobLabels
	r.mu.Unlock()
	if grant.node == "" || grant.job != task.JobKey || grant.node != node || !labelPolicyAllows(policy) {
		return ctx
	}
	if grant.extension == "" && taskType == joblabelscope.Builtin+":"+joblabelscope.Builtin {
		return joblabelscope.WithBuiltin(ctx, r.jobLabelEdit(ctx, task, original, grant))
	}
	if grant.extension != "" && taskType == extensions.ExecutionOpType+":"+extensions.ExecutionOpType {
		return joblabelscope.WithExtension(ctx, joblabelscope.Extension{Selector: grant.extension, Edit: r.jobLabelEdit(ctx, task, original, grant)})
	}
	return ctx
}

func (r *Runtime) jobLabelEdit(active context.Context, task jobworkflow.TaskContext, original *lease, grant jobLabelPolicy) joblabelscope.EditFunc {
	return func(call context.Context, edit joblabel.Edit) (joblabel.State, error) {
		if err := call.Err(); err != nil {
			return joblabel.State{}, err
		}
		r.mu.Lock()
		live := r.leases[task.JobKey]
		policy := r.jobLabels
		r.mu.Unlock()
		if active.Err() != nil || task.Context().Err() != nil || live == nil || live.leaseState != original.leaseState || live.Job().JobKey != task.JobKey || live.LeaseID() == "" {
			return joblabel.State{}, jobdb.ErrExecutionLeaseLost
		}
		if grant != policy || original.Delegation() != nil || live.Delegation() != nil {
			return joblabel.State{}, jobdb.ErrLeasePermissionDenied
		}
		if expiry, ok := live.ExecutionLease.(interface{ LeaseExpiry() time.Time }); ok && !expiry.LeaseExpiry().After(time.Now()) {
			return joblabel.State{}, jobdb.ErrExecutionLeaseLost
		}
		port, ok := live.ExecutionLease.(jobdb.JobMetadataLease)
		if !ok {
			return joblabel.State{}, jobdb.ErrJobMetadataUnsupported
		}
		writeCtx, cancel := context.WithCancel(call)
		defer cancel()
		stopActive := context.AfterFunc(active, cancel)
		defer stopActive()
		stopTask := context.AfterFunc(task.Context(), cancel)
		defer stopTask()
		if active.Err() != nil || task.Context().Err() != nil {
			return joblabel.State{}, jobdb.ErrExecutionLeaseLost
		}
		return joblabel.EditWithLease(writeCtx, port, edit)
	}
}
