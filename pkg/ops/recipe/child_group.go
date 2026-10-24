package recipe

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/segmentio/ksuid"
)

const (
	ChildGroupStartOpType           = "recipe.child_group.start"
	ChildGroupRunAndGetResultOpType = "recipe.child_group.run_and_get_result"

	childGroupJobIDNamespace = "colony2.recipe-child-group/v2"
)

type ChildGroupStartInput struct {
	Wait      WaitPolicy                `json:"wait,omitempty"`
	Mode      string                    `json:"mode,omitempty"`
	GitRef    string                    `json:"git_ref,omitempty" default:"${{ has(context.git.hash) && context.git.hash != \"\" ? context.git.hash : context.git.ref }}"`
	Children  []ChildGroupChildSpec     `json:"children,omitempty"`
	Aggregate ChildGroupAggregateConfig `json:"aggregate,omitempty"`
}

type ChildGroupChildSpec struct {
	Wait       *WaitPolicy            `json:"wait,omitempty"`
	Key        string                 `json:"key,omitempty"`
	Index      int                    `json:"index,omitempty"`
	Recipe     string                 `json:"recipe,omitempty"`
	CellName   string                 `json:"cell_name,omitempty"`
	Required   bool                   `json:"required"`
	Skipped    bool                   `json:"skipped,omitempty"`
	SkipReason string                 `json:"skip_reason,omitempty"`
	GitRef     string                 `json:"git_ref,omitempty"`
	Inputs     map[string]interface{} `json:"inputs,omitempty" secret:"forward"`
	Artifacts  []recipeartifacts.Ref  `json:"artifacts,omitempty"`
}

type ChildGroupAggregateConfig struct {
	Shape    string `json:"shape,omitempty"`
	Artifact string `json:"artifact,omitempty"`
}

type ChildGroupStepState struct {
	Mode      string                    `json:"mode,omitempty"`
	GitRef    string                    `json:"git_ref,omitempty"`
	Children  []ChildGroupChildRecord   `json:"children,omitempty"`
	Aggregate ChildGroupAggregateConfig `json:"aggregate,omitempty"`
}

type ChildGroupChildRecord struct {
	TenantID    string                         `json:"tenant_id,omitempty"`
	GoalFailure *jobdb.GoalFailure             `json:"goal_failure,omitempty"`
	GoalID      string                         `json:"goal_id,omitempty"`
	Result      ResultSource                   `json:"result"`
	Wait        WaitPolicy                     `json:"wait,omitempty"`
	Key         string                         `json:"key,omitempty"`
	Index       int                            `json:"index,omitempty"`
	Recipe      string                         `json:"recipe,omitempty"`
	CellName    string                         `json:"cell_name,omitempty"`
	Required    bool                           `json:"required"`
	Status      string                         `json:"status,omitempty"`
	JobID       string                         `json:"job_id,omitempty"`
	Outputs     map[string]interface{}         `json:"outputs,omitempty"`
	Artifacts   map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
	Error       string                         `json:"error,omitempty"`
	SkipReason  string                         `json:"skip_reason,omitempty"`
}

type ChildGroupSummary struct {
	Total          int `json:"total"`
	Started        int `json:"started"`
	Completed      int `json:"completed"`
	Failed         int `json:"failed"`
	FailedRequired int `json:"failed_required"`
	FailedOptional int `json:"failed_optional"`
	Skipped        int `json:"skipped"`
	StartFailed    int `json:"start_failed"`
	Required       int `json:"required"`
	Optional       int `json:"optional"`
}

type ChildGroupOutput struct {
	ChildGoalIDs         []string                       `json:"child_goal_ids"`
	RequiredChildGoalIDs []string                       `json:"required_child_goal_ids"`
	OptionalChildGoalIDs []string                       `json:"optional_child_goal_ids"`
	FailedChildGoalIDs   []string                       `json:"failed_child_goal_ids"`
	Ok                   bool                           `json:"ok"`
	Mode                 string                         `json:"mode,omitempty"`
	ChildJobIDs          []string                       `json:"child_job_ids,omitempty"`
	RequiredChildJobIDs  []string                       `json:"required_child_job_ids,omitempty"`
	OptionalChildJobIDs  []string                       `json:"optional_child_job_ids,omitempty"`
	FailedChildJobIDs    []string                       `json:"failed_child_job_ids,omitempty"`
	Children             []ChildGroupChildRecord        `json:"children,omitempty"`
	Summary              ChildGroupSummary              `json:"summary"`
	Aggregate            map[string]interface{}         `json:"aggregate,omitempty"`
	Warnings             []map[string]interface{}       `json:"warnings,omitempty"`
	BlockingIssues       []map[string]interface{}       `json:"blocking_issues,omitempty"`
	AggregateArtifact    string                         `json:"aggregate_artifact,omitempty"`
	Artifacts            map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
}

func getChildGroupOps() []ops.RegisterableOp {
	return []ops.RegisterableOp{
		ops.NewOp().
			WithType(ChildGroupStartOpType).
			AddStep("start", ops.NewStepWithDeps[ChildGroupStartInput, ChildGroupOutput](startChildGroupOnly)).
			BuildOrPanic(),
		ops.NewOp().
			WithType(ChildGroupRunAndGetResultOpType).
			AddStep("start", ops.NewStepWithDeps[ChildGroupStartInput, ChildGroupStepState](startChildGroup)).
			AddStep("await", ops.NewStepWithDeps[ChildGroupStepState, ChildGroupStepState](awaitChildGroup)).
			AddStep("collect", ops.NewStepWithDeps[ChildGroupStepState, ChildGroupOutput](collectChildGroup)).
			BuildOrPanic(),
	}
}

func startChildGroupOnly(deps ops.OpDependencies, ctx context.Context, input ChildGroupStartInput) (ChildGroupOutput, error) {
	state, err := startChildGroup(deps, ctx, input)
	if err != nil {
		return ChildGroupOutput{}, err
	}
	return buildChildGroupOutput(deps, state)
}

func startChildGroup(deps ops.OpDependencies, ctx context.Context, input ChildGroupStartInput) (ChildGroupStepState, error) {
	for i := range input.Children {
		policy := input.Wait
		if input.Children[i].Wait != nil {
			policy = *input.Children[i].Wait
		}
		effective, err := policy.normalized(false)
		if err != nil {
			return ChildGroupStepState{}, fmt.Errorf("child %d: %w", i, err)
		}
		input.Children[i].Wait = &effective
	}
	mode := normalizeChildGroupMode(input.Mode)
	state := ChildGroupStepState{
		Mode:      mode,
		GitRef:    strings.TrimSpace(input.GitRef),
		Aggregate: normalizeChildGroupAggregate(input.Aggregate),
		Children:  make([]ChildGroupChildRecord, 0, len(input.Children)),
	}
	if _, err := childGroupAggregateShape(state.Aggregate.Shape); err != nil {
		return ChildGroupStepState{}, err
	}

	parentJobKey := deps.JobTool().GetJobKey()
	invocation := currentInvocation(deps)
	if invocation == "" {
		return ChildGroupStepState{}, fmt.Errorf("child submission requires a logical task invocation ID")
	}
	parentContext := parentContextFromDeps(deps)
	recipeSourceRepo, recipeSourceRef := currentRecipeSource(deps)
	gitContext := deps.GitContext()

	for i, child := range input.Children {
		child.Index = i
		child.Key = strings.TrimSpace(child.Key)
		if child.Key == "" {
			child.Key = fmt.Sprintf("%d", i)
		}

		record := ChildGroupChildRecord{
			Wait:       input.Wait,
			Key:        child.Key,
			Index:      i,
			Recipe:     strings.TrimSpace(child.Recipe),
			CellName:   strings.TrimSpace(child.CellName),
			Required:   child.Required,
			SkipReason: strings.TrimSpace(child.SkipReason),
			Artifacts:  map[string]recipeartifacts.Ref{},
		}
		if child.Wait != nil {
			record.Wait = *child.Wait
		}
		if child.Skipped {
			record.Status = "skipped"
			if record.SkipReason == "" {
				record.SkipReason = "when evaluated false"
			}
			state.Children = append(state.Children, record)
			continue
		}
		if record.Recipe == "" {
			record.Status = "start_failed"
			record.Error = "recipe is required"
			state.Children = append(state.Children, record)
			continue
		}

		cellName := record.CellName
		if cellName == "" {
			cellName = gitContext.CellName
			if gitContext.Workspace != nil {
				cellName = gitContext.Workspace.Cell
			}
		}
		gitRef := strings.TrimSpace(child.GitRef)
		if gitRef == "" {
			gitRef = state.GitRef
		}
		jobID := deterministicChildGroupJobID(parentJobKey, invocation, child.Key, i)
		job, err := recipeToStart(parentJobKey.TenantId, SingleRecipe{
			Name:      record.Recipe,
			CellName:  cellName,
			Inputs:    cloneMap(child.Inputs),
			Artifacts: append([]recipeartifacts.Ref(nil), child.Artifacts...),
			Git: SingleRecipeGit{
				BaseRepo: gitContext.BaseRepo,
				BaseRef:  gitContext.BaseRef,
				BaseHash: gitContext.ResolvedBaseHash,
				Author:   gitContext.GitAuthor,
			},
		}, recipeSourceRepo, recipeSourceRef, gitRef, jobID, parentContext)
		if err != nil {
			record.Status = "start_failed"
			record.Error = err.Error()
			state.Children = append(state.Children, record)
			continue
		}
		key, err := startRecipeChildJob(ctx, deps, deps.WorkflowControl(), job)
		if err != nil {
			record.Status = "start_failed"
			record.Error = err.Error()
			state.Children = append(state.Children, record)
			continue
		}
		record.Status = "started"
		record.JobID = key.JobID
		record.TenantID = key.TenantID
		record.GoalID = key.GoalID
		state.Children = append(state.Children, record)
	}

	return state, nil
}

func awaitChildGroup(deps ops.OpDependencies, ctx context.Context, state ChildGroupStepState) (ChildGroupStepState, error) {
	for i := range state.Children {
		child := &state.Children[i]
		if child.Status != "started" {
			continue
		}
		binding, err := awaitChild(deps, ctx, StartedJob{JobID: child.JobID, GoalID: child.GoalID, Wait: child.Wait}, false)
		if err != nil {
			if binding.State == jobdb.DependencyFailed || errors.Is(err, jobdb.ErrGoalFailed) || errors.Is(err, jobdb.ErrJobFailed) || errors.Is(err, jobdb.ErrJobCancelled) {
				child.Status = "failed"
				child.Error = err.Error()
				var failed *jobdb.GoalFailedError
				if errors.As(err, &failed) {
					child.GoalFailure = &failed.Failure
				}
				continue
			}
			return state, err
		}
		child.Result = ResultSource{Goal: binding.Target.GoalID, Job: binding.SatisfiedByJobID}
	}

	return state, nil
}

func collectChildGroup(deps ops.OpDependencies, ctx context.Context, state ChildGroupStepState) (ChildGroupOutput, error) {
	for i := range state.Children {
		child := &state.Children[i]
		if child.Status != "started" || strings.TrimSpace(child.JobID) == "" {
			continue
		}
		if child.Result.Job == "" {
			return ChildGroupOutput{}, fmt.Errorf("child %s has no recorded result binding", child.Key)
		}
		result, err := getChildGroupRecipeOutput(deps, ctx, child.Result.Job)
		if err != nil {
			if !errors.Is(err, jobdb.ErrJobFailed) && !errors.Is(err, jobdb.ErrJobCancelled) {
				return ChildGroupOutput{}, err
			}
			child.Status = "failed"
			if errors.Is(err, jobdb.ErrJobCancelled) {
				child.Status = "cancelled"
			}
			child.Error = err.Error()
			continue
		}
		child.Status = "completed"
		child.Outputs = result.Outputs
		child.Artifacts = result.Artifacts
	}
	return buildChildGroupOutput(deps, state)
}

type childGroupRecipeOutput struct {
	Outputs   map[string]interface{}
	Artifacts map[string]recipeartifacts.Ref
}

func getChildGroupRecipeOutput(deps ops.OpDependencies, ctx context.Context, jobID string) (childGroupRecipeOutput, error) {
	var zero childGroupRecipeOutput
	data, err := deps.WorkflowControl().JobResult(ctx, jobdb.JobKey{TenantId: deps.JobTool().GetJobKey().TenantId, JobId: jobID})
	if err != nil {
		return zero, err
	}

	decoded, err := decodeRecipeJobOutput(deps, data)
	if err != nil {
		return zero, err
	}
	return childGroupRecipeOutput{Outputs: decoded.Outputs, Artifacts: decoded.Artifacts}, nil
}

func buildChildGroupOutput(deps ops.OpDependencies, state ChildGroupStepState) (ChildGroupOutput, error) {
	shape, err := childGroupAggregateShape(state.Aggregate.Shape)
	if err != nil {
		return ChildGroupOutput{}, err
	}

	out := ChildGroupOutput{
		Ok:        true,
		Mode:      normalizeChildGroupMode(state.Mode),
		Children:  append([]ChildGroupChildRecord(nil), state.Children...),
		Aggregate: map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{},
	}

	for _, child := range state.Children {
		out.Summary.Total++
		if child.Required {
			out.Summary.Required++
		} else {
			out.Summary.Optional++
		}
		if child.JobID != "" {
			out.ChildJobIDs = append(out.ChildJobIDs, child.JobID)
			out.ChildGoalIDs = append(out.ChildGoalIDs, child.GoalID)
			if child.Required {
				out.RequiredChildJobIDs = append(out.RequiredChildJobIDs, child.JobID)
				out.RequiredChildGoalIDs = append(out.RequiredChildGoalIDs, child.GoalID)
			} else {
				out.OptionalChildJobIDs = append(out.OptionalChildJobIDs, child.JobID)
				out.OptionalChildGoalIDs = append(out.OptionalChildGoalIDs, child.GoalID)
			}
		}
		for name, ref := range child.Artifacts {
			if name == "" {
				name = ref.NameValue()
			}
			if name != "" {
				out.Artifacts[name] = ref
			}
		}

		switch child.Status {
		case "started":
			out.Summary.Started++
		case "completed":
			out.Summary.Completed++
		case "failed", "cancelled":
			out.Summary.Failed++
			out.FailedChildJobIDs = append(out.FailedChildJobIDs, child.JobID)
			out.FailedChildGoalIDs = append(out.FailedChildGoalIDs, child.GoalID)
			issue := childGroupIssue(child, "child_failed", child.Error)
			if child.Required {
				out.Summary.FailedRequired++
				out.Ok = false
				out.BlockingIssues = append(out.BlockingIssues, issue)
			} else {
				out.Summary.FailedOptional++
				out.Warnings = append(out.Warnings, issue)
			}
		case "skipped":
			out.Summary.Skipped++
		case "start_failed":
			out.Summary.StartFailed++
			issue := childGroupIssue(child, "child_start_failed", child.Error)
			if child.Required {
				out.Summary.FailedRequired++
				out.Ok = false
				out.BlockingIssues = append(out.BlockingIssues, issue)
			} else {
				out.Summary.FailedOptional++
				out.Warnings = append(out.Warnings, issue)
			}
		}

		if child.Required && child.Status == "completed" {
			if ok, exists := child.Outputs["ok"].(bool); exists && !ok {
				out.Ok = false
				out.BlockingIssues = append(out.BlockingIssues, childGroupIssue(child, "child_not_ok", "required child returned ok=false"))
			}
		} else if !child.Required && child.Status == "completed" {
			if ok, exists := child.Outputs["ok"].(bool); exists && !ok {
				out.Warnings = append(out.Warnings, childGroupIssue(child, "optional_child_not_ok", "optional child returned ok=false"))
			}
		}
	}

	switch shape {
	case "none":
	case "job_ids":
		out.Aggregate["jobs"] = childGroupJobAggregate(state.Children)
	case "review_pack":
		out.Aggregate["children"] = childGroupReviewChildren(state.Children)
		out.Aggregate["summary"] = out.Summary
		for _, child := range state.Children {
			blockingIssues := childGroupOutputIssues(child, "blocking_issues")
			if child.Required {
				out.BlockingIssues = append(out.BlockingIssues, blockingIssues...)
			} else {
				out.Warnings = append(out.Warnings, blockingIssues...)
			}
			out.Warnings = append(out.Warnings, childGroupOutputIssues(child, "warnings")...)
		}
		if len(out.BlockingIssues) > 0 {
			out.Ok = false
		}
		out.Aggregate["ok"] = out.Ok
		out.Aggregate["blocking_issues"] = out.BlockingIssues
		out.Aggregate["warnings"] = out.Warnings
	}

	if state.Aggregate.Artifact != "" {
		payload, err := json.MarshalIndent(out.Aggregate, "", "  ")
		if err != nil {
			return ChildGroupOutput{}, err
		}
		if err := deps.AddOutputArtifact(jobdb.NewArtifactFromBytes(state.Aggregate.Artifact, payload)); err != nil {
			return ChildGroupOutput{}, err
		}
		out.AggregateArtifact = state.Aggregate.Artifact
	}
	return out, nil
}

func childGroupStartedJobIDs(children []ChildGroupChildRecord) []string {
	ids := make([]string, 0, len(children))
	for _, child := range children {
		if child.Status == "started" && strings.TrimSpace(child.JobID) != "" {
			ids = append(ids, child.JobID)
		}
	}
	return ids
}

func childGroupIssue(child ChildGroupChildRecord, code string, message string) map[string]interface{} {
	return map[string]interface{}{
		"code":         code,
		"message":      strings.TrimSpace(message),
		"child":        child.Key,
		"job_id":       child.JobID,
		"goal_id":      child.GoalID,
		"result":       child.Result,
		"goal_failure": child.GoalFailure,
		"recipe":       child.Recipe,
		"required":     child.Required,
	}
}

func childGroupOutputIssues(child ChildGroupChildRecord, key string) []map[string]interface{} {
	if child.Outputs == nil {
		return nil
	}
	raw, ok := child.Outputs[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		issue := map[string]interface{}{
			"child":        child.Key,
			"job_id":       child.JobID,
			"goal_id":      child.GoalID,
			"result":       child.Result,
			"goal_failure": child.GoalFailure,
			"recipe":       child.Recipe,
			"required":     child.Required,
		}
		switch typed := item.(type) {
		case map[string]interface{}:
			for k, v := range typed {
				issue[k] = v
			}
		case string:
			issue["message"] = typed
		default:
			issue["message"] = fmt.Sprint(typed)
		}
		out = append(out, issue)
	}
	return out
}

func childGroupJobAggregate(children []ChildGroupChildRecord) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(children))
	for _, child := range children {
		if child.JobID == "" {
			continue
		}
		out = append(out, map[string]interface{}{
			"key":          child.Key,
			"index":        child.Index,
			"recipe":       child.Recipe,
			"job_id":       child.JobID,
			"goal_id":      child.GoalID,
			"result":       child.Result,
			"goal_failure": child.GoalFailure,
			"required":     child.Required,
			"status":       child.Status,
		})
	}
	return out
}

func childGroupReviewChildren(children []ChildGroupChildRecord) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(children))
	for _, child := range children {
		entry := map[string]interface{}{
			"key":          child.Key,
			"index":        child.Index,
			"recipe":       child.Recipe,
			"job_id":       child.JobID,
			"goal_id":      child.GoalID,
			"result":       child.Result,
			"goal_failure": child.GoalFailure,
			"required":     child.Required,
			"status":       child.Status,
		}
		if child.Error != "" {
			entry["error"] = child.Error
		}
		if child.SkipReason != "" {
			entry["skip_reason"] = child.SkipReason
		}
		if child.Outputs != nil {
			entry["outputs"] = child.Outputs
		}
		out = append(out, entry)
	}
	return out
}

func normalizeChildGroupMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "run_and_get_result"
	}
	return mode
}

func normalizeChildGroupAggregate(config ChildGroupAggregateConfig) ChildGroupAggregateConfig {
	config.Shape = strings.TrimSpace(config.Shape)
	if config.Shape == "" {
		config.Shape = "none"
	}
	config.Artifact = strings.TrimSpace(config.Artifact)
	return config
}

func childGroupAggregateShape(shape string) (string, error) {
	shape = strings.TrimSpace(shape)
	if shape == "" {
		shape = "none"
	}
	switch shape {
	case "none", "job_ids", "review_pack":
		return shape, nil
	default:
		return "", fmt.Errorf("unsupported child_group aggregate shape %q", shape)
	}
}

func deterministicChildGroupJobID(parentJobKey jobdb.JobKey, invocation string, key string, recipeIndex int) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return deterministicChildJobID(parentJobKey, invocation, recipeIndex)
	}
	hasher := sha256.New()
	hasher.Write([]byte(childGroupJobIDNamespace))
	hasher.Write([]byte{0})
	hasher.Write([]byte(parentJobKey.TenantId))
	hasher.Write([]byte{0})
	hasher.Write([]byte(parentJobKey.JobId))
	hasher.Write([]byte{0})
	hasher.Write([]byte(invocation))
	hasher.Write([]byte{0})
	hasher.Write([]byte(key))

	sum := hasher.Sum(nil)
	var jobID ksuid.KSUID
	copy(jobID[:], sum[:len(jobID)])
	return jobID.String()
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
