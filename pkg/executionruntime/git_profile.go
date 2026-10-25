package executionruntime

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (w *guardedTask) runWithGitProfile(tc jobworkflow.TaskContext, input jobdb.TaskData, original *lease) (jobdb.TaskData, error) {
	var request struct {
		Permissions *permissions.State   `json:"permissions,omitempty"`
		GitProfile  gitprofile.Selection `json:"git_profile"`
	}
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '{' {
		if err = json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
	}
	snapshot, err := tc.GetConfiguration(tc.Context())
	if err != nil {
		return nil, err
	}
	resolve := func(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
		r := w.runtime
		r.mu.Lock()
		current := r.leases[tc.JobKey]
		r.mu.Unlock()
		if current == nil || current.leaseState != original.leaseState || tc.Context().Err() != nil {
			return jobdb.SecretResolveResult{}, jobdb.ErrExecutionLeaseLost
		}
		// ExecutionLease resolution is fresh; workflow TaskContext resolution is
		// attempt-cached. Snapshot the renewed lease without holding the lock over I/O.
		result, err := current.ExecutionLease.ResolveSecrets(ctx, names)
		r.mu.Lock()
		after := r.leases[tc.JobKey]
		r.mu.Unlock()
		if after == nil || after.leaseState != original.leaseState || tc.Context().Err() != nil || ctx.Err() != nil {
			return jobdb.SecretResolveResult{}, jobdb.ErrExecutionLeaseLost
		}
		return result, err
	}
	_, err = jobref.FromHandle(tc.Job())
	if err != nil {
		return nil, err
	}
	// Omission means restricted defaults, including for source-resolution tasks.
	policy := permissions.State{Version: permissions.Version}
	if request.Permissions != nil {
		policy = *request.Permissions
	}
	names, err := gitprofile.CredentialNames(request.GitProfile, snapshot)
	if err != nil {
		return nil, err
	}
	grant, err := policy.Resolve(names)
	if err != nil {
		return nil, err
	}
	access, scopeContext, cancel, err := w.runtime.restrictedAccess(tc, original, grant, resolve)
	if err != nil {
		return nil, err
	}
	defer cancel()
	resolve = access.Resolve
	scopeContext = jobref.WithScope(scopeContext, jobref.Scope{Submit: access.Submit})
	scope, err := gitprofile.NewScope(scopeContext, request.GitProfile, snapshot, resolve, "", tc.JobKey.TenantId)
	if err != nil {
		return nil, err
	}
	releaseRedaction := scope.RetainRedaction()
	defer releaseRedaction()
	defer scope.Close()
	scope.AllowSecrets(access.Grant().Secrets.Resolve)
	unbind := gitprofile.BindTask(tc, scope)
	defer unbind()
	out, err := w.TaskWorker.Run(tc, input)
	err = scope.Error(err)
	if out != nil {
		raw, e := out.GetData()
		if e != nil {
			return nil, e
		}
		if e = scope.CheckOutput(raw); e != nil {
			return nil, e
		}
	}
	return out, err
}

// Submission must use the renewed owner lease, just like fresh secret reads.
func (r *Runtime) submitOnCurrentLease(ctx context.Context, tc jobworkflow.TaskContext, original *lease, job jobdb.SubmitJob) (jobdb.JobHandle, error) {
	r.mu.Lock()
	current := r.leases[tc.JobKey]
	r.mu.Unlock()
	if current == nil || current.leaseState != original.leaseState || tc.Context().Err() != nil {
		return jobdb.JobHandle{}, jobdb.ErrExecutionLeaseLost
	}
	return current.ExecutionLease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
}
