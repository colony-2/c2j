package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

const WorkspaceResolutionTaskType = "recipe_workspace_resolve"

type WorkspaceResolutionInput struct {
	// OwnerSnapshot loads implicit hook bases from the trusted job handle instead
	// of the mutable registry. This is not an authored workspace option.
	OwnerSnapshot bool                 `json:"owner_snapshot,omitempty"`
	Repo          string               `json:"repo,omitempty"`
	Permissions   *permissions.State   `json:"permissions,omitempty"`
	GitProfile    gitprofile.Selection `json:"git_profile,omitzero"`
	Cell          string               `json:"cell"`
	Initial       string               `json:"initial,omitempty"`
	Ref           string               `json:"ref,omitempty"`
	ScopeID       string               `json:"scope_id"`
	ParentScopeID string               `json:"parent_scope_id,omitempty"`
	Resolution    cellref.Context      `json:"resolution"`
}
type WorkspaceResolutionOutput struct {
	Workspace contextual.WorkspaceContext `json:"workspace"`
	Git       contextual.GitBaseContext   `json:"git"`
}
type workspaceResolutionWorker struct{}

func NewWorkspaceResolutionTaskWorker() jobworkflow.TaskWorker { return workspaceResolutionWorker{} }
func (workspaceResolutionWorker) Name() string                 { return WorkspaceResolutionTaskType }
func (w workspaceResolutionWorker) Run(tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, data)
}

func (w workspaceResolutionWorker) RunTask(local context.Context, tc jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	ctx, cancel := context.WithCancel(local)
	defer cancel()
	return resolveWorkspaceTask(ctx, data, tc.Job())
}
func resolveWorkspaceTask(ctx context.Context, data jobdb.TaskData, owners ...jobdb.JobHandle) (jobdb.TaskData, error) {
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	var req WorkspaceResolutionInput
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if permissions.FromContext(ctx) == nil {
		return nil, fmt.Errorf("permission-scoped workspace requires executionruntime.WrapTaskWorker")
	}
	if req.ScopeID == "" {
		return nil, fmt.Errorf("workspace scope ID is required")
	}
	if req.OwnerSnapshot {
		if len(owners) != 1 || owners[0].CellID == "" || owners[0].CellRepo == "" {
			return nil, fmt.Errorf("owning job snapshot is required")
		}
		req.Cell, req.Repo = owners[0].Cell, owners[0].CellRepo
		req.Resolution = cellref.Context{}
	}
	target, err := req.Resolution.Resolve(req.Cell, req.Ref)
	if err != nil {
		return nil, err
	}
	if req.Repo != "" {
		target.Repository, err = jobdb.NormalizeCellRepo(req.Repo)
		if err != nil {
			return nil, err
		}
	}
	if req.Initial != "empty" && !req.OwnerSnapshot {
		scope, ok := jobref.FromContext(ctx)
		if !ok {
			return nil, fmt.Errorf("lease-bound cell resolver is required")
		}
		cell, err := cellref.ResolveTarget(ctx, scope.ResolveCell, target.Cell, target.Repository)
		if err != nil {
			return nil, err
		}
		target.Cell, target.Repository = cell.Cell, cell.CellRepo
	}
	if target.Repository == "" {
		return nil, fmt.Errorf("workspace requires a registered cell or explicit repo")
	}
	target.Repository, err = NormalizeGitRepositorySource(target.Repository)
	if err != nil {
		return nil, err
	}
	if req.Initial != "" {
		if req.Initial != "empty" {
			return nil, fmt.Errorf("invalid workspace.initial %q", req.Initial)
		}
		if _, err := common.ExecuteGitCommand(ctx, "", "check-ref-format", "refs/heads/"+strings.TrimPrefix(target.Ref, "refs/heads/")); err != nil {
			return nil, fmt.Errorf("empty workspace requires a branch: %w", err)
		}
		return jobdb.NewTaskData(WorkspaceResolutionOutput{
			Workspace: contextual.WorkspaceContext{Cell: target.Cell, ScopeID: req.ScopeID, ParentScopeID: req.ParentScopeID, Initial: "empty"},
			Git:       contextual.GitBaseContext{BaseRepo: target.Repository, BaseRef: target.Ref},
		})
	}
	resolved, err := selectorcache.Default().Resolve(ctx, selectorcache.ResolveRequest{RepositoryURL: target.Repository, Ref: target.Ref})
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %q: %w", req.Cell, err)
	}
	policy := ""
	if !isFullGitHash(target.Ref) {
		policy = contextual.WorkspaceFollowPerTask
	}
	return jobdb.NewTaskData(WorkspaceResolutionOutput{
		Workspace: contextual.WorkspaceContext{Cell: target.Cell, ScopeID: req.ScopeID, ParentScopeID: req.ParentScopeID, InitialHash: resolved.Commit, RefPolicy: policy},
		Git:       contextual.GitBaseContext{BaseRepo: target.Repository, BaseRef: target.Ref, ResolvedBaseHash: resolved.Commit},
	})
}

func enterWorkspace(ctx workflow.Context, parent *template.ResolutionContext, meta recipe.NodeMetadata) (*template.ResolutionContext, error) {
	spec := meta.Workspace
	if spec == nil {
		return parent, nil
	}
	render := func(value string, required bool) (string, error) {
		if value == "" && !required {
			return "", nil
		}
		out, err := parent.ResolveValueWithLocals(value, nil)
		if err != nil {
			return "", err
		}
		if out == nil && parent.Options.Mode == template.ModeValidate {
			return "validation-workspace", nil
		}
		str, ok := out.(string)
		if !ok || strings.TrimSpace(str) == "" {
			return "", fmt.Errorf("workspace selector must resolve to a nonempty string")
		}
		return strings.TrimSpace(str), nil
	}
	cell, err := render(spec.Cell, true)
	if err != nil {
		return nil, fmt.Errorf("workspace.cell: %w", err)
	}
	repo, err := render(spec.Repo, false)
	if err != nil {
		return nil, fmt.Errorf("workspace.repo: %w", err)
	}
	ref, err := render(spec.Ref, false)
	if err != nil {
		return nil, fmt.Errorf("workspace.ref: %w", err)
	}
	incoming := parent.TaskExecutionContext()
	resolution := cellref.Context{SelfRepo: incoming.GitTask.BaseRepo, SelfRef: incoming.GitTask.BaseRef}
	if incoming.CellResolution != nil {
		resolution = *incoming.CellResolution
	}
	req := WorkspaceResolutionInput{Cell: cell, Repo: repo, Ref: ref, Initial: spec.Initial, ScopeID: parent.NextWorkspaceID(meta.ID), Resolution: resolution}
	req.GitProfile = parent.GitProfile
	p := parent.Permissions
	req.Permissions = &p
	if incoming.Workspace != nil {
		req.ParentScopeID = incoming.Workspace.ScopeID
	}
	var result WorkspaceResolutionOutput
	if parent.Options.Mode == template.ModeValidate {
		result = WorkspaceResolutionOutput{Workspace: contextual.WorkspaceContext{Cell: cell, ScopeID: req.ScopeID, ParentScopeID: req.ParentScopeID, InitialHash: strings.Repeat("0", 40)}, Git: contextual.GitBaseContext{BaseRepo: cell, BaseRef: ref, ResolvedBaseHash: strings.Repeat("0", 40)}}
		if spec.Initial == "empty" {
			result.Workspace.Initial = "empty"
			result.Workspace.InitialHash = ""
			result.Git.ResolvedBaseHash = ""
		}
	} else {
		if ctx.StageNodeExecution != nil {
			ctx.StageNodeExecution(parent.ExecutionNeeds)
		}
		policy := jobdb.RunPolicy{TotalTimeout: jobdb.AsDuration(time.Hour)}
		if meta.Retry != nil {
			policy.Retry = *meta.Retry
		}
		out, e := workflow.ValueCall(ctx.JobContext, "workspace:"+req.ScopeID, WorkspaceResolutionTaskType, policy, req)
		if e != nil {
			return nil, e
		}
		raw, e := out.GetData()
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &result); e != nil {
			return nil, e
		}
		if result.Workspace.ScopeID != req.ScopeID || (result.Workspace.InitialHash == "" && result.Workspace.Initial != "empty") || result.Workspace.Initial != req.Initial || result.Git.BaseRepo == "" {
			return nil, fmt.Errorf("invalid workspace resolution result")
		}
	}
	result.Git.GitAuthor = incoming.GitTask.GitAuthor
	return parent.WithWorkspace(result.Workspace, result.Git)
}

type workspaceBody func(workflow.Context, *template.ResolutionContext, recipe.NodeMetadata) error

func withNodeWorkspace(ctx workflow.Context, parent *template.ResolutionContext, meta recipe.NodeMetadata, body workspaceBody) error {
	if !ctx.WorkspaceSnapshots {
		forwarding := newThinPackForwardingJobContext(ctx.JobContext)
		forwarding.scopedMode = true
		ctx.JobContext = forwarding
		ctx.WorkspaceSnapshots = true
	}
	scoped, err := enterWorkspace(ctx, parent, meta)
	if err != nil {
		if isExecutionControlError(err) {
			return err
		}
		failure := normalizeRuntimeFailure(err, parent, meta, recipe.FailureNodeOp, "workspace")
		return newRecipeFailureError(failure, err)
	}
	meta.Workspace = nil
	err = body(ctx, scoped, meta)
	parent.PublishWorkspaceResult(scoped)
	return err
}
