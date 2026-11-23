package executionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func (w *guardedTask) runWithGitProfile(base context.Context, tc jobworkflow.TaskContext, input jobdb.TaskData, original *lease) (result jobdb.TaskData, runErr error) {
	redactor := secrets.RedactorFromContext(base)
	if redactor == nil {
		redactor = secrets.NewRedactor()
	}
	retain := redactor.NewSession()
	defer retain.Close()
	defer redactor.Close()
	stop := context.AfterFunc(tc.Context(), redactor.Close)
	defer stop()
	defer func() { runErr = resourceretry.Redact(runErr, redactor.Error) }()
	local := secrets.WithRedactor(base, redactor)
	kind := metricsruntime.Kind(w.TaskWorker)
	if kind == "" {
		kind = metricsruntime.OutputKindFromContext(base)
	}
	if kind != "" && metricsruntime.Current(local) == nil {
		var timing *metricsruntime.Timing
		local, timing = metricsruntime.Begin(local, w.Name(), redactor.CheckOutput)
		defer func() { result, runErr = metricsruntime.FinishTask(timing, kind, result, runErr) }()
	}

	local = original.submissionContext(local)
	local = execution.WithEnvironment(local, w.runtime.Allocation)
	local = execution.WithRepositoryAdmission(local, func(ctx context.Context, defaults execution.Requirements) error {
		w.runtime.mu.Lock()
		staged := original.demand
		w.runtime.mu.Unlock()
		d := execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "resolved"}
		if staged != nil {
			d = *staged
		}
		effective, err := execution.RepositoryFallback(defaults, d.Effective)
		if err != nil {
			return err
		}
		d.Effective = effective
		if err := original.checkBase(tc.Context(), effective, taskBaseSource(tc.InvocationID, tc.Step)+" (repository defaults)"); err != nil {
			return err
		}
		mismatches, err := execution.Compare(effective, w.runtime.Allocation)
		if err != nil {
			return err
		}
		w.runtime.Stage(tc.JobKey, d)
		if len(mismatches) > 0 {
			_, err = original.yieldEnvironment(tc, mismatches)
			if err != nil && !jobworkflow.IsExecutionSuspended(err) {
				original.restoreDemand(staged)
			}
			return err
		}
		return nil
	})
	local, gitStderr := gitprofile.WithStderrLog(local)
	defer func() {
		if extensionwait.IsDiscard(runErr) || resourceretry.Is(runErr) {
			result = nil
			return
		}
		stderr := gitStderr.Bytes()
		if len(stderr) == 0 {
			return
		}
		var payload json.RawMessage
		var artifacts []jobdb.Artifact
		var err error
		if result != nil {
			payload, err = result.GetData()
			if err == nil {
				artifacts, err = result.GetArtifacts()
			}
			if err != nil {
				runErr = errors.Join(runErr, err)
				return
			}
		}
		artifacts = append(artifacts, jobdb.NewArtifactFromBytes(gitprofile.StderrArtifactName, stderr))
		result, err = jobdb.NewTaskData(payload, artifacts...)
		runErr = errors.Join(runErr, err)
	}()
	tc.Logger = secrets.MaskLogger(tc.Logger, redactor)
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
	access, scopeContext, cancel, err := w.runtime.restrictedAccess(local, tc, original, grant, resolve, policy)
	if err != nil {
		return nil, err
	}
	defer cancel()
	fresh := resolve
	scopeContext = permissions.WithInputResolver(scopeContext, func(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
		values, err := fresh(ctx, names)
		if err != nil {
			return values, err
		}
		for _, name := range names {
			if value, ok := values.Secrets[name]; ok {
				if err := redactor.Register(value.Value); err != nil {
					return jobdb.SecretResolveResult{}, err
				}
			}
		}
		return values, nil
	})
	resolve = access.Resolve
	resolveCell := func(call context.Context, name string, lookup bool) (jobdb.ResolvedCell, error) {
		if err := access.Context().Err(); err != nil {
			return jobdb.ResolvedCell{}, err
		}
		w.runtime.mu.Lock()
		current := w.runtime.leases[tc.JobKey]
		w.runtime.mu.Unlock()
		if current == nil || current.leaseState != original.leaseState || tc.Context().Err() != nil {
			return jobdb.ResolvedCell{}, jobdb.ErrExecutionLeaseLost
		}
		if lookup {
			return current.ExecutionLease.LookupCell(call, name)
		}
		return current.ExecutionLease.ResolveCell(call, name)
	}
	scopeContext = jobref.WithScope(scopeContext, jobref.Scope{
		Submit: access.Submit,
		LookupCell: func(call context.Context, name string) (jobdb.ResolvedCell, error) {
			return resolveCell(call, name, true)
		},
		ResolveCell: func(call context.Context, name string) (jobdb.ResolvedCell, error) {
			if !grant.Jobs.Submit || (grant.Jobs.CellNames != nil && !slices.Contains(grant.Jobs.CellNames, name)) {
				return jobdb.ResolvedCell{}, jobdb.ErrLeasePermissionDenied
			}
			return resolveCell(call, name, false)
		},
	})
	scopeContext = executioncatalog.WithPolicy(scopeContext, original.catalogPolicy())
	scope, err := gitprofile.NewScope(scopeContext, request.GitProfile, snapshot, resolve, "", tc.JobKey.TenantId)
	if err != nil {
		return nil, err
	}
	defer scope.Close()
	scope.AllowSecrets(access.Grant().Secrets.Resolve)
	out, err := task.RunLocal(w.TaskWorker, scope.Context(), tc, input)
	err = resourceretry.Redact(err, scope.Error)
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
	if err := executioncatalog.CheckSubmission(current.submissionContext(ctx), job); err != nil {
		return jobdb.JobHandle{}, err
	}
	return current.ExecutionLease.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
}
