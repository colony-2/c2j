package executionruntime

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/gitprofile"
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
	scopeContext := tc.Context()
	var access *permissions.Access
	if request.Permissions != nil {
		names, err := gitprofile.CredentialNames(request.GitProfile, snapshot)
		if err != nil {
			return nil, err
		}
		grant, err := request.Permissions.Resolve(names)
		if err != nil {
			return nil, err
		}
		var cancel context.CancelFunc
		access, scopeContext, cancel, err = w.runtime.restrictedAccess(tc, original, grant, resolve)
		if err != nil {
			return nil, err
		}
		defer cancel()
		resolve = access.Resolve
	}
	scope, err := gitprofile.NewScope(scopeContext, request.GitProfile, snapshot, resolve, "", tc.JobKey.TenantId)
	if err != nil {
		return nil, err
	}
	releaseRedaction := scope.RetainRedaction()
	defer releaseRedaction()
	defer scope.Close()
	if access != nil {
		scope.AllowSecrets(access.Grant().Secrets.Resolve)
	}
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
