package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/colony-2/c2j/internal/jsonexact"
	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/jobcontext"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/segmentio/ksuid"
	"gopkg.in/yaml.v3"
)

const (
	parallelPinTask         = "parallel-workspace"
	parallelLaunchTask      = "parallel-launch"
	parallelCollectTask     = "parallel-collect"
	parallelContextArtifact = "__c2j_parallel_context.json"
)

type parallelGitState struct {
	Base      contextual.GitBaseContext   `json:"base"`
	Commit    contextual.GitCommitContext `json:"commit"`
	Workspace contextual.WorkspaceContext `json:"workspace"`
	Restore   *jobdb.ArtifactKey          `json:"restore,omitempty"`
}

type parallelPinRequest struct {
	State       parallelGitState     `json:"state"`
	Permissions *permissions.State   `json:"permissions,omitempty"`
	GitProfile  gitprofile.Selection `json:"git_profile,omitzero"`
}

type parallelLaunch struct {
	BranchID string                `json:"branch_id"`
	Recipe   string                `json:"recipe_yaml"`
	Start    workflowctl.StartJob  `json:"start"`
	Lexical  parallelBranchContext `json:"lexical"`
}

type parallelBranchContext struct {
	Inputs         map[string]any           `json:"inputs,omitempty"`
	Vars           map[string]any           `json:"vars,omitempty"`
	ResolvedRefs   map[string]string        `json:"resolved_refs,omitempty"`
	Transition     *template.TransitionData `json:"transition,omitempty"`
	ExecutionNeeds execution.Requirements   `json:"execution_needs,omitzero"`
	ToolScopes     []toolenv.Scope          `json:"tool_scopes,omitempty"`
}

type parallelResult struct {
	JobID     string                         `json:"job_id,omitempty"`
	Status    string                         `json:"status"`
	Error     string                         `json:"error,omitempty"`
	Outputs   map[string]any                 `json:"outputs,omitempty"`
	Artifacts map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
}

type parallelResults struct {
	Branches map[string]parallelResult `json:"branches"`
}

type parallelWorker struct {
	name    string
	control workflowctl.WorkflowControl
}

func (worker parallelWorker) Name() string { return worker.name }

func (worker parallelWorker) Run(task jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	return worker.RunTask(task.Context(), task, data)
}

func (worker parallelWorker) RunTask(ctx context.Context, task jobworkflow.TaskContext, data jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := data.GetData()
	if err != nil {
		return nil, err
	}
	switch worker.name {
	case parallelPinTask:
		var request parallelPinRequest
		if err := jsonexact.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		return pinParallelWorkspace(ctx, request.State)
	case parallelLaunchTask:
		var request parallelLaunch
		if err := jsonexact.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		return worker.launch(ctx, task, request)
	case parallelCollectTask:
		var results parallelResults
		if err := jsonexact.Unmarshal(raw, &results); err != nil {
			return nil, err
		}
		return worker.collect(ctx, task, results.Branches)
	default:
		return nil, fmt.Errorf("unsupported parallel task %q", worker.name)
	}
}

func pinParallelWorkspace(ctx context.Context, state parallelGitState) (jobdb.TaskData, error) {
	if state.Commit.PersistHash != "" && state.Commit.PersistHash != state.Commit.ParentHash && state.Restore == nil {
		return nil, fmt.Errorf("parallel requires the snapshot for workspace commit %s", state.Commit.PersistHash)
	}
	commit := state.Commit.ParentHash
	if commit == "" && !state.Workspace.FollowsRef() && (state.Workspace.ScopeID != "" || isFullGitHash(state.Base.BaseRef)) {
		commit = state.Base.ResolvedBaseHash
	}
	if commit == "" && state.Base.BaseRepo != "" && state.Workspace.Initial != "empty" {
		ref := state.Commit.ParentRef
		if ref == "" {
			ref = state.Base.BaseRef
		}
		resolved, err := selectorcache.Default().Resolve(ctx, selectorcache.ResolveRequest{RepositoryURL: state.Base.BaseRepo, Ref: ref})
		if err != nil {
			return nil, fmt.Errorf("pin parallel workspace: %w", err)
		}
		commit = resolved.Commit
	}
	if state.Commit.PersistHash != "" && state.Commit.PersistHash == commit && state.Restore == nil {
		state.Commit.PersistHash = ""
	}
	state.Base.ResolvedBaseHash = commit
	state.Workspace.InitialHash = commit
	state.Workspace.RefPolicy = ""
	if state.Commit.PersistHash == "" {
		state.Commit.ParentRef = commit
		state.Commit.ParentHash = commit
	}
	return jobdb.NewTaskData(state)
}

func (worker parallelWorker) launch(ctx context.Context, task jobworkflow.TaskContext, request parallelLaunch) (jobdb.TaskData, error) {
	if task.InvocationID == "" {
		return nil, fmt.Errorf("parallel launch requires a durable task invocation ID")
	}
	start := request.Start
	digest := sha256.Sum256([]byte(task.JobKey.TenantId + "\x00" + task.JobKey.JobId + "\x00" + task.InvocationID))
	var bytes [20]byte
	copy(bytes[:], digest[:20])
	start.JobID = ksuid.KSUID(bytes).String()
	start.JobContext.Workspace.ScopeID = "parallel:" + start.JobID
	owner := task.Job()
	start.Parent = &jobcontext.Parent{TenantID: task.JobKey.TenantId, JobID: task.JobKey.JobId, CellID: owner.CellID, CellName: owner.Cell, CellRepo: owner.CellRepo, OpTaskType: parallelLaunchTask}
	if start.Cell == "" {
		start.Cell = owner.Cell
	}
	embedded, err := recipe.LoadInternalRecipeFromString([]byte(request.Recipe))
	if err != nil {
		return parallelStartRejected(err)
	}
	lexical, err := json.Marshal(request.Lexical)
	if err != nil {
		return parallelStartRejected(err)
	}
	start.Artifacts = []jobdb.Artifact{jobdb.NewArtifactFromBytes(parallelContextArtifact, lexical)}
	job, err := starter.PrepareParallelBranchJob(ctx, start, *embedded)
	if err != nil {
		return parallelStartRejected(err)
	}
	if err := executioncatalog.CheckSubmission(ctx, job); err != nil {
		return parallelLaunchError(err)
	}
	child, err := task.SubmitJob(ctx, job)
	if err != nil {
		return parallelLaunchError(err)
	}
	if child.JobKey.JobId != start.JobID || child.JobKey.TenantId != start.TenantId {
		return nil, fmt.Errorf("parallel submission returned a different job for branch %q", request.BranchID)
	}
	return jobdb.NewTaskData(parallelResult{JobID: start.JobID, Status: "started"})
}

func parallelStartRejected(err error) (jobdb.TaskData, error) {
	return jobdb.NewTaskData(parallelResult{Status: "start_failed", Error: err.Error()})
}

type nonRetryableParallelLaunchError struct{ error }

func (err nonRetryableParallelLaunchError) Unwrap() error  { return err.error }
func (nonRetryableParallelLaunchError) NonRetryable() bool { return true }

func parallelLaunchError(err error) (jobdb.TaskData, error) {
	if errors.Is(err, jobdb.ErrJobSchemaValidation) || errors.Is(err, jobdb.ErrJobSchemaArchived) || errors.Is(err, jobdb.ErrJobSchemaNotFound) || errors.Is(err, jobdb.ErrLeasePermissionDenied) || errors.Is(err, jobdb.ErrExistingJobMismatch) {
		return parallelStartRejected(err)
	}
	var rejection jobdb.AppError
	if errors.As(err, &rejection) {
		code, _ := rejection.Payload.Attrs["code"].(string)
		switch code {
		case executioncatalog.Unsupported, executioncatalog.UnsupportedRequirements:
			return parallelStartRejected(err)
		}
	}
	var systemErr jobdb.SystemError
	if errors.As(err, &systemErr) && !systemErr.Payload.Retryable {
		return nil, nonRetryableParallelLaunchError{err}
	}
	return nil, jobdb.ClassifyRuntimeIO(err, "c2j.parallel_submit", nil, nil)
}

func (worker parallelWorker) collect(ctx context.Context, task jobworkflow.TaskContext, results map[string]parallelResult) (jobdb.TaskData, error) {
	dependencies := make([]jobdb.Dependency, 0, len(results))
	for _, id := range slices.Sorted(maps.Keys(results)) {
		branch := results[id]
		if branch.JobID != "" {
			dependencies = append(dependencies, jobdb.Dependency{Target: jobdb.DependencyTarget{JobID: branch.JobID}, Condition: jobdb.JobPrereqComplete})
		}
	}
	if len(dependencies) == 0 {
		return jobdb.NewTaskData(parallelResults{Branches: results})
	}
	if _, err := task.AwaitDependencies(dependencies...); err != nil {
		return nil, err
	}
	inspector, ok := worker.control.(workflowctl.JobInspector)
	if !ok {
		return nil, fmt.Errorf("parallel requires job inspection support")
	}
	for id, branch := range results {
		if branch.JobID == "" {
			continue
		}
		info, err := inspector.InspectJob(ctx, jobdb.JobKey{TenantId: task.JobKey.TenantId, JobId: branch.JobID})
		if err != nil {
			return nil, jobdb.ClassifyRuntimeIO(err, "c2j.parallel_collect", nil, nil)
		}
		if !info.Terminal {
			return nil, fmt.Errorf("parallel branch %q has not finished", id)
		}
		branch.Status, branch.Error = info.Status, info.FailureMessage
		if info.JobStatus == jobdb.JobStatusExpired {
			branch.Status = "timed_out"
		}
		if info.Output != nil {
			branch.Outputs, branch.Artifacts, err = recipeops.ReadRecipeResult(info.Output)
			if err != nil {
				return nil, err
			}
		}
		results[id] = branch
	}
	return jobdb.NewTaskData(parallelResults{Branches: results})
}

func readParallelContext(ctx context.Context, artifacts []jobdb.Artifact) (*parallelBranchContext, error) {
	var scope *parallelBranchContext
	for _, artifact := range artifacts {
		if artifact.Name() != parallelContextArtifact {
			continue
		}
		if scope != nil {
			return nil, fmt.Errorf("duplicate parallel branch context")
		}
		raw, err := artifact.Bytes(ctx)
		if err != nil {
			return nil, jobdb.ClassifyRuntimeIO(err, "c2j.parallel_context", nil, nil)
		}
		scope = new(parallelBranchContext)
		if err := jsonexact.Unmarshal(raw, scope); err != nil {
			return nil, err
		}
		if scope.Inputs != nil {
			scope.Inputs = jsonexact.Native(scope.Inputs).(map[string]any)
		}
		if scope.Vars != nil {
			scope.Vars = jsonexact.Native(scope.Vars).(map[string]any)
		}
		if scope.Transition != nil {
			if payload := scope.Transition.Payload; payload != nil {
				scope.Transition.Payload = jsonexact.Native(payload).(map[string]any)
			}
			for failure := scope.Transition.Failure; failure != nil; failure = failure.Cause {
				if failure.Attrs != nil {
					failure.Attrs = jsonexact.Native(failure.Attrs).(map[string]any)
				}
			}
		}
	}
	if scope == nil {
		return nil, fmt.Errorf("parallel branch context is missing")
	}
	return scope, nil
}

func (d DefaultRecipeExecutor) executeParallel(ctx workflow.Context, parent *template.ResolutionContext, metadata recipe.NodeMetadata, definition recipe.ParallelData) error {
	return executeNodeScope(ctx, parent, metadata, template.ScopeSequence, "parallel", func(inner workflow.Context, enclosing *template.ResolutionContext, meta recipe.NodeMetadata) error {
		if meta.Workspace == nil {
			return d.innerParallel(inner, enclosing, meta, definition)
		}
		return withNodeWorkspace(inner, enclosing, meta, func(inner workflow.Context, enclosing *template.ResolutionContext, meta recipe.NodeMetadata) error {
			return d.innerParallel(inner, enclosing, meta, definition)
		})
	})
}

func (d DefaultRecipeExecutor) innerParallel(ctx workflow.Context, parent *template.ResolutionContext, metadata recipe.NodeMetadata, definition recipe.ParallelData) error {
	if err := definition.Parallel.Validate(); err != nil {
		return err
	}
	inputs := map[string]any{}
	var err error
	if metadata.Internal != nil && metadata.Internal.BoundInputs != nil {
		inputs = metadata.Internal.BoundInputs
	} else {
		inputs, err = parent.ResolveCompositeInputs(metadata.Inputs)
		if err != nil {
			return err
		}
		inputs, err = prepareCompositeInputs(metadata, inputs)
		if err != nil {
			return err
		}
	}
	scope, err := parent.NewChildContext(template.ScopeSequence, metadata, "parallel", inputs)
	if err != nil {
		return err
	}
	if ctx.ScopeState != nil {
		ctx.ScopeState(scope)
	}
	if err := scope.ResolveVars(metadata.Vars); err != nil {
		return err
	}
	if err := scope.ResolveExecutionNeeds(metadata.ExecutionNeeds); err != nil {
		return err
	}
	if handled, err := runIncludeMock(ctx, parent, scope, metadata, inputs); handled {
		return err
	}
	results := make(map[string]parallelResult, len(definition.Parallel.Branches))
	var active []recipe.Node
	for _, branch := range definition.Parallel.Branches {
		meta := branch.GetMetadata()
		run, err := scope.EvaluateCEL(meta.When.String())
		if err != nil {
			return fmt.Errorf("parallel branch %q condition: %w", meta.ID, err)
		}
		if run || scope.Options.Mode == template.ModeValidate && scope.Options.ValidationMode == string(ValidateAll) {
			active = append(active, branch)
		} else {
			results[meta.ID] = parallelResult{Status: "skipped"}
		}
	}
	if scope.Options.Mode == template.ModeValidate {
		inner := ctx
		inner.ConstBranch = true
		scope.EffectiveConst = true
		for _, branch := range active {
			if err := d.self().ExecuteNode(inner, scope, &branch); err != nil {
				return err
			}
			id := branch.GetMetadata().ID
			out := scope.TemplateData.Sequence[id]
			results[id] = parallelResult{Status: "completed", Outputs: out.Outputs, Artifacts: out.Artifacts}
			delete(scope.TemplateData.Sequence, id)
		}
	} else if len(active) > 0 {
		results, err = launchParallelBranches(ctx, scope, active, results)
		if err != nil {
			return err
		}
	}
	var failed []string
	scope.TemplateData.Parallel = make(map[string]template.StepOutput, len(results))
	for _, id := range slices.Sorted(maps.Keys(results)) {
		branch := results[id]
		scope.TemplateData.Parallel[id] = template.StepOutput{Outputs: branch.Outputs, Artifacts: branch.Artifacts}
		if branch.Status != "completed" && branch.Status != "skipped" {
			diagnostic := fmt.Sprintf("%s (%s)", id, branch.Status)
			if branch.Error != "" {
				diagnostic += ": " + branch.Error
			}
			failed = append(failed, diagnostic)
		}
	}
	if len(failed) > 0 {
		err := fmt.Errorf("parallel branches were unsuccessful: %s", strings.Join(failed, ", "))
		failure := normalizeRuntimeFailure(err, scope, metadata, recipe.FailureNodeSequence, "")
		failure.Attrs = map[string]any{"parallel": results}
		return newRecipeFailureError(failure, err)
	}
	outputs, err := scope.ResolveMap(definition.Outputs)
	if err != nil {
		return err
	}
	if err := validateExpectedOutputs(metadata.ExpectOutputs, outputs); err != nil {
		return err
	}
	return completeNodeScope(ctx, scope, template.ExecutionOutput{Outputs: outputs})
}

func launchParallelBranches(ctx workflow.Context, scope *template.ResolutionContext, branches []recipe.Node, results map[string]parallelResult) (map[string]parallelResult, error) {
	exec := scope.TaskExecutionContext()
	job := exec.JobContext()
	state := parallelGitState{Base: job.GitBase, Commit: scope.GetGitCommitContext()}
	if job.Workspace != nil {
		state.Workspace = *job.Workspace
	}
	if forwarder, ok := ctx.JobContext.(*thinpackForwarder); ok {
		restore, err := forwarder.parallelRestore(state.Workspace.ScopeID)
		if err != nil {
			return nil, err
		}
		state.Restore = restore
	}
	request := parallelPinRequest{State: state, GitProfile: scope.GitProfile, Permissions: &scope.Permissions}
	if err := parallelCall(ctx, "parallel-pin", parallelPinTask, request, &state); err != nil {
		return nil, err
	}
	state.Workspace.ParentScopeID = state.Workspace.ScopeID
	job = contextual.JobContext{
		GitBase: state.Base, Workspace: &state.Workspace, InitialCommit: &state.Commit, RestoreArtifact: state.Restore,
		CellResolution: job.CellResolution, RecipeSource: job.RecipeSource, GitProfile: scope.GitProfile,
		InheritedPermissions: &scope.Permissions,
		Workflow:             contextual.WorkflowContext{CellName: state.Workspace.Cell, JobID: contextual.JobIdSentinel},
	}
	lexical := parallelBranchContext{
		Inputs: scope.TemplateData.ContainerInputs, Vars: scope.TemplateData.Vars, ResolvedRefs: scope.Options.ResolvedGitRefs,
		ExecutionNeeds: scope.ExecutionNeeds,
	}
	if transition := scope.TemplateData.Transition; transition.From != "" || transition.To != "" || len(transition.Payload) > 0 || transition.Failure != nil {
		lexical.Transition = &transition
	}
	for _, tools := range scope.ToolScopes {
		if tools.ID != "recipe" && tools.ID != "job" {
			lexical.ToolScopes = append(lexical.ToolScopes, tools)
		}
	}
	for _, branch := range branches {
		id := branch.GetMetadata().ID
		if ctx.RecordStatus == nil {
			branch = recipe.WithoutNodeStatus(branch)
		}
		branchName := "parallel-branch"
		meta := recipe.RecipeMetadata{NodeMetadata: recipe.NodeMetadata{ID: branchName, Internal: ctx.ParallelDefinition.Internal}, Version: "1", Extensions: ctx.ParallelDefinition.Extensions, SchemaVersion: ctx.ParallelDefinition.SchemaVersion, SourceHooks: ctx.SourceHooks}
		embedded := recipe.Recipe{RecipeImpl: &recipe.RecipeSequence{RecipeMetadata: meta, SequenceData: recipe.SequenceData{Sequence: []recipe.Node{branch}}}}
		raw, err := yaml.Marshal(&embedded)
		if err != nil {
			return nil, err
		}
		start := workflowctl.StartJob{Title: "Parallel branch: " + id, TenantId: ctx.GetJobKey().TenantId, RecipeName: branchName, JobContext: job,
			Cell: state.Workspace.Cell, CellRepo: state.Base.BaseRepo, GitRef: state.Commit.ParentRef}
		if ctx.RecordStatus != nil {
			start.StatusHistoryVersion = workflowctl.StatusHistoryVersion
		}
		for _, name := range sortedRefMapKeys(exec.Artifacts) {
			start.ArtifactRefs = append(start.ArtifactRefs, exec.Artifacts[name])
		}
		var result parallelResult
		if err := parallelCall(ctx, "parallel-launch:"+id, parallelLaunchTask, parallelLaunch{BranchID: id, Recipe: string(raw), Start: start, Lexical: lexical}, &result); err != nil {
			return nil, err
		}
		results[id] = result
	}
	var collected parallelResults
	if err := parallelCall(ctx, "parallel-collect", parallelCollectTask, parallelResults{Branches: results}, &collected); err != nil {
		return nil, err
	}
	return collected.Branches, nil
}

func parallelCall(ctx workflow.Context, site, name string, input, output any) error {
	var policy jobdb.RunPolicy
	if name == parallelLaunchTask {
		policy = jobdb.DefaultRunPolicy()
		policy.Retry.NonRetryableErrorTypes = nil
	}
	data, err := workflow.ValueCall(ctx.JobContext, site, name, policy, input)
	if err != nil {
		return err
	}
	raw, err := data.GetData()
	if err != nil {
		return err
	}
	return coretask.DecodeInternal(raw, output)
}

func parallelDisallowedOp(op string) bool {
	switch op {
	case "git_persist_commit", "git_restore_commit", "git_push", "push", "merge", "squash", "rebase", "thinpackrebase", "squashrebasemerge":
		return true
	default:
		return false
	}
}
