package executionruntime

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (w *guardedTask) runWithGitProfile(tc jobworkflow.TaskContext, input jobdb.TaskData, original *lease) (jobdb.TaskData, error) {
	var request struct {
		GitProfile gitprofile.Selection `json:"git_profile"`
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
	scope, err := gitprofile.NewScope(tc.Context(), request.GitProfile, snapshot, resolve, "", tc.JobKey.TenantId)
	if err != nil {
		return nil, err
	}
	releaseRedaction := scope.RetainRedaction()
	defer releaseRedaction()
	defer scope.Close()
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
