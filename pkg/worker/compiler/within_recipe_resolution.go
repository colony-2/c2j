package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/swf-go/pkg/swf"
)

const WithinRecipeResolutionTaskType = "recipe_within_resolution"

type withinRecipeResolutionTaskInput struct {
	Selectors        []string          `json:"selectors,omitempty"`
	RepositorySource string            `json:"repository_source,omitempty"`
	RepositoryRef    string            `json:"repository_ref,omitempty"`
	ResolvedGitRefs  map[string]string `json:"resolved_git_refs,omitempty"`
}

type WithinRecipeResolutionResult struct {
	ResolvedSelectors map[string]string `json:"resolved_selectors,omitempty"`
	ResolvedGitRefs   map[string]string `json:"resolved_git_refs,omitempty"`
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
		ResolvedRefs:     cloneResolvedGitRefs(req.ResolvedGitRefs),
	})
	if err != nil {
		return nil, err
	}
	return swf.NewTaskData(resolved)
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

func resolveWithinRecipeSelectors(ctx workflow.Context, rec recipe.Recipe, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, execOpts ExecutionOptions) (WithinRecipeResolutionResult, error) {
	if len(execOpts.ResolvedSelectors) > 0 {
		return WithinRecipeResolutionResult{
			ResolvedSelectors: cloneResolvedSelectors(execOpts.ResolvedSelectors),
			ResolvedGitRefs:   cloneResolvedGitRefs(execOpts.ResolvedGitRefs),
		}, nil
	}

	req := buildWithinRecipeResolutionTaskInput(&rec, execCtx, commitContext, execOpts)
	if req == nil {
		return WithinRecipeResolutionResult{ResolvedGitRefs: cloneResolvedGitRefs(execOpts.ResolvedGitRefs)}, nil
	}

	taskInput, err := swf.NewTaskData(req)
	if err != nil {
		return WithinRecipeResolutionResult{}, fmt.Errorf("encode within recipe resolution input: %w", err)
	}
	taskOutput, err := ctx.DoTask(swf.RunPolicy{}, WithinRecipeResolutionTaskType, taskInput)
	if err != nil {
		return WithinRecipeResolutionResult{}, fmt.Errorf("within recipe resolution task failed: %w", err)
	}
	parsed, err := ParseWithinRecipeResolutionTaskData(taskOutput)
	if err != nil {
		return WithinRecipeResolutionResult{}, err
	}
	return WithinRecipeResolutionResult{
		ResolvedSelectors: cloneResolvedSelectors(parsed.ResolvedSelectors),
		ResolvedGitRefs:   cloneResolvedGitRefs(parsed.ResolvedGitRefs),
	}, nil
}

func buildWithinRecipeResolutionTaskInput(rec *recipe.Recipe, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, execOpts ExecutionOptions) *withinRecipeResolutionTaskInput {
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
		ResolvedGitRefs:  cloneResolvedGitRefs(execOpts.ResolvedGitRefs),
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

func resolveSelectors(ctx context.Context, selectors []string, opts extops.ResolveOptions) (WithinRecipeResolutionResult, error) {
	refPins := cloneResolvedGitRefs(opts.ResolvedRefs)
	if len(selectors) == 0 {
		return WithinRecipeResolutionResult{ResolvedGitRefs: refPins}, nil
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
				return WithinRecipeResolutionResult{}, fmt.Errorf("local selector %q requires repository source and ref for replayable execution", selector)
			}
		}
		resolved, err := extops.ResolvePath(ctx, selector, extops.ResolveOptions{
			RepositorySource: repoSource,
			RepositoryRef:    repoRef,
			ResolvedRefs:     refPins,
		})
		if err != nil {
			return WithinRecipeResolutionResult{}, fmt.Errorf("resolve selector %q: %w", selector, err)
		}
		if pinned := strings.TrimSpace(resolved.ResolvedSelector); pinned != "" {
			out[selector] = pinned
		}
		if key, ok, err := selectorRepoRefKey(selector, repoSource, repoRef); err != nil {
			return WithinRecipeResolutionResult{}, err
		} else if ok && strings.TrimSpace(resolved.ResolvedCommit) != "" {
			if refPins == nil {
				refPins = make(map[string]string)
			}
			refPins[key] = strings.TrimSpace(resolved.ResolvedCommit)
		}
	}
	if len(out) == 0 {
		out = nil
	}
	return WithinRecipeResolutionResult{
		ResolvedSelectors: out,
		ResolvedGitRefs:   refPins,
	}, nil
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

func cloneResolvedGitRefs(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func selectorRepoRefKey(selector string, repoSource string, repoRef string) (string, bool, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", false, nil
	}
	if extops.IsLocalSelector(selector) {
		if strings.TrimSpace(repoSource) == "" || strings.TrimSpace(repoRef) == "" {
			return "", false, nil
		}
		normalized, err := extops.NormalizeGitRepositorySourceForSelector(repoSource)
		if err != nil {
			return "", true, err
		}
		return selectorcache.RepoRefKey(normalized, repoRef), true, nil
	}
	return extops.GitSelectorRepoRefKey(selector)
}
