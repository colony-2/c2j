package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
)

const WithinRecipeResolutionTaskType = "recipe_within_resolution"

type withinRecipeResolutionTaskInput struct {
	Selectors        []string `json:"selectors,omitempty"`
	RepositorySource string   `json:"repository_source,omitempty"`
	RepositoryRef    string   `json:"repository_ref,omitempty"`
}

type WithinRecipeResolutionResult struct {
	ResolvedSelectors map[string]string `json:"resolved_selectors,omitempty"`
}

type withinRecipeResolutionTaskWorker struct{}

func newWithinRecipeResolutionTaskWorker() swf.TaskWorker {
	return withinRecipeResolutionTaskWorker{}
}

func (w withinRecipeResolutionTaskWorker) Name() string {
	return WithinRecipeResolutionTaskType
}

func (w withinRecipeResolutionTaskWorker) Run(taskCtx swf.TaskContext, input swf.TaskData) (swf.TaskData, error) {
	req, err := parseWithinRecipeResolutionTaskInput(input)
	if err != nil {
		return nil, err
	}

	ctx, cancel := workerops.NewTaskExecutionContext(taskCtx)
	defer cancel()
	resolved, err := resolveSelectors(ctx, req.Selectors, extops.ResolveOptions{
		RepositorySource: strings.TrimSpace(req.RepositorySource),
		RepositoryRef:    strings.TrimSpace(req.RepositoryRef),
	})
	if err != nil {
		return nil, err
	}
	return swf.NewTaskData(WithinRecipeResolutionResult{ResolvedSelectors: resolved})
}

func ParseWithinRecipeResolutionTaskData(td swf.TaskData) (*WithinRecipeResolutionResult, error) {
	if td == nil {
		return nil, fmt.Errorf("within recipe resolution task data is required")
	}
	raw, err := td.GetData()
	if err != nil {
		return nil, err
	}
	return ParseWithinRecipeResolutionJSON(raw)
}

func ParseWithinRecipeResolutionJSON(raw []byte) (*WithinRecipeResolutionResult, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("within recipe resolution payload is empty")
	}

	result := WithinRecipeResolutionResult{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode within recipe resolution payload: %w", err)
	}
	return &result, nil
}

func parseWithinRecipeResolutionTaskInput(input swf.TaskData) (*withinRecipeResolutionTaskInput, error) {
	if input == nil {
		return nil, fmt.Errorf("within recipe resolution input is required")
	}
	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	req := withinRecipeResolutionTaskInput{}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode within recipe resolution input: %w", err)
	}
	return &req, nil
}

func resolveWithinRecipeSelectors(ctx workflow.Context, rec recipe.Recipe, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, execOpts ExecutionOptions) (map[string]string, error) {
	if len(execOpts.ResolvedSelectors) > 0 {
		return cloneResolvedSelectors(execOpts.ResolvedSelectors), nil
	}

	req := buildWithinRecipeResolutionTaskInput(&rec, execCtx, commitContext)
	if req == nil {
		return nil, nil
	}

	taskInput, err := swf.NewTaskData(req)
	if err != nil {
		return nil, fmt.Errorf("encode within recipe resolution input: %w", err)
	}
	taskOutput, err := ctx.DoTask(swf.RunPolicy{}, WithinRecipeResolutionTaskType, taskInput)
	if err != nil {
		return nil, fmt.Errorf("within recipe resolution task failed: %w", err)
	}
	parsed, err := ParseWithinRecipeResolutionTaskData(taskOutput)
	if err != nil {
		return nil, err
	}
	return cloneResolvedSelectors(parsed.ResolvedSelectors), nil
}

func buildWithinRecipeResolutionTaskInput(rec *recipe.Recipe, execCtx contextual.JobContext, commitContext contextual.GitCommitContext) *withinRecipeResolutionTaskInput {
	if rec == nil || rec.RecipeImpl == nil {
		return nil
	}

	selectors := collectSelectorsNeedingResolution(rec)
	if len(selectors) == 0 {
		return nil
	}

	repoSource, repoRef := selectorResolutionRepository(execCtx, commitContext)
	return &withinRecipeResolutionTaskInput{
		Selectors:        selectors,
		RepositorySource: repoSource,
		RepositoryRef:    repoRef,
	}
}

func collectSelectorsNeedingResolution(rec *recipe.Recipe) []string {
	seen := map[string]struct{}{}
	collect := func(selector string) {
		selector = strings.TrimSpace(selector)
		if !selectorNeedsResolution(selector) {
			return
		}
		seen[selector] = struct{}{}
	}

	metadata := recipeMetadataPtr(rec)
	if metadata != nil {
		for i := range metadata.Extensions.Functions {
			collect(metadata.Extensions.Functions[i].Selector)
		}
	}

	switch typed := rec.RecipeImpl.(type) {
	case *recipe.RecipeOp:
		collect(typed.OpData.Op)
	case *recipe.RecipeSequence:
		collectSelectorsInNodes(typed.SequenceData.Sequence, collect)
	case *recipe.RecipeState:
		collectSelectorsInStateMap(typed.StateMachineData.States, collect)
	case *recipe.RecipeChildGroup:
		// child_group recipe names are recipe selectors, not op selectors.
	}

	if len(seen) == 0 {
		return nil
	}

	out := make([]string, 0, len(seen))
	for selector := range seen {
		out = append(out, selector)
	}
	sort.Strings(out)
	return out
}

func collectSelectorsInNodes(nodes []recipe.Node, collect func(string)) {
	for i := range nodes {
		collectSelectorsInNode(&nodes[i], collect)
	}
}

func collectSelectorsInNode(node *recipe.Node, collect func(string)) {
	if node == nil || node.NodeImpl == nil {
		return
	}
	switch typed := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		collect(typed.OpData.Op)
	case *recipe.NodeSequence:
		collectSelectorsInNodes(typed.SequenceData.Sequence, collect)
	case *recipe.NodeState:
		collectSelectorsInStateMap(typed.StateMachineData.States, collect)
	case *recipe.NodeChildGroup:
		// child_group recipe names are recipe selectors, not op selectors.
	}
}

func collectSelectorsInStateMap(states *recipe.StateMap, collect func(string)) {
	if states == nil {
		return
	}
	for name := range states.States {
		state := states.States[name]
		collectSelectorsInNode(&state.Node, collect)
	}
}

func selectorNeedsResolution(selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" || !extops.IsSelector(selector) {
		return false
	}
	if extops.IsLocalSelector(selector) {
		return false
	}
	return !selectorUsesPinnedGitRef(selector)
}

func selectorUsesPinnedGitRef(selector string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" || !strings.HasPrefix(selector, "git+") {
		return false
	}
	atIdx := strings.LastIndex(selector, "@")
	if atIdx <= 0 || atIdx == len(selector)-1 {
		return false
	}
	return isFullGitHash(selector[atIdx+1:])
}

func selectorResolutionRepository(execCtx contextual.JobContext, commitContext contextual.GitCommitContext) (string, string) {
	repoSource := strings.TrimSpace(execCtx.RecipeSource.Repo)
	if repoSource == "" {
		repoSource = strings.TrimSpace(execCtx.GitBase.BaseRepo)
	}

	repoRef := strings.TrimSpace(execCtx.RecipeSource.Ref)
	if repoRef == "" {
		repoRef = strings.TrimSpace(execCtx.GitBase.ResolvedBaseHash)
	}
	if repoRef == "" {
		repoRef = strings.TrimSpace(commitContext.ParentRef)
	}
	if repoRef == "" {
		repoRef = strings.TrimSpace(execCtx.GitBase.BaseRef)
	}

	return repoSource, repoRef
}

func resolveSelectors(ctx context.Context, selectors []string, opts extops.ResolveOptions) (map[string]string, error) {
	if len(selectors) == 0 {
		return nil, nil
	}

	repoSource := strings.TrimSpace(opts.RepositorySource)
	repoRef := strings.TrimSpace(opts.RepositoryRef)
	out := make(map[string]string, len(selectors))
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if selector == "" {
			continue
		}
		if extops.IsLocalSelector(selector) {
			if repoSource == "" || repoRef == "" {
				return nil, fmt.Errorf("local selector %q requires repository source and ref for replayable execution", selector)
			}
		}
		resolved, err := extops.ResolvePath(ctx, selector, extops.ResolveOptions{
			RepositorySource: repoSource,
			RepositoryRef:    repoRef,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve selector %q: %w", selector, err)
		}
		if pinned := strings.TrimSpace(resolved.ResolvedSelector); pinned != "" {
			out[selector] = pinned
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func cloneResolvedSelectors(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
