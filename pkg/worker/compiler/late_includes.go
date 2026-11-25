package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"

	recipecel "github.com/colony-2/c2j/pkg/cel"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// Separate contracts leave historical operation and hook declarations unchanged.
// These types deliberately have no ':'; they do not restore or publish workspaces.
const lateSelectTaskType = "recipe_late_select"
const lateCaptureTaskType = "recipe_late_capture"

type lateSelection struct {
	Invocation         string                     `json:"invocation"`
	Selector           string                     `json:"selector"`
	Repository         string                     `json:"repository"`
	Path               string                     `json:"path"`
	Commit             string                     `json:"commit"`
	Declaration        recipe.LateIncludeMetadata `json:"declaration"`
	PermissionBoundary bool                       `json:"permission_boundary"`
	TrustedSource      bool                       `json:"trusted_source"`
}

func (s lateSelection) resolution() RecipeSourceResolution {
	return RecipeSourceResolution{SourceKind: RecipeSourceKindGit, SubmittedSelector: s.Selector, ResolvedSelector: fmt.Sprintf("git+%s//%s@%s", s.Repository, s.Path, s.Commit), ResolvedCommit: s.Commit, WasAlreadyPinned: true}
}

type lateCaptureInput struct {
	Selection     lateSelection        `json:"selection"`
	Permissions   *permissions.State   `json:"permissions"`
	GitProfile    gitprofile.Selection `json:"git_profile"`
	CallsitePerms *permissions.Spec    `json:"callsite_perms,omitempty"`
	ProjectID     string               `json:"project_id"`
}
type lateCapture struct {
	Selection          lateSelection                `json:"selection"`
	RecipeYAMLArtifact string                       `json:"recipe_yaml_artifact"`
	ContentSHA256      string                       `json:"content_sha256"`
	Functions          WithinRecipeResolutionResult `json:"functions"`
}

func validateLateInclude(n *recipe.NodeInclude, stateKey string) error {
	if n.Include.Bind != "late" {
		return fmt.Errorf("include.bind must be late; omit bind for eager includes")
	}
	if n.Include.IfMissing != nil || strings.HasPrefix(n.Include.Recipe, "cell://") {
		return fmt.Errorf("late include cannot use cell hooks or if_missing")
	}
	if strings.TrimSpace(n.ID) == "" && stateKey == "" {
		return fmt.Errorf("late sequence include requires an explicit id")
	}
	if strings.TrimSpace(n.Include.Recipe) == "" {
		return fmt.Errorf("late include requires a recipe selector")
	}
	if err := n.Perms.Validate(); err != nil {
		return err
	}
	_, err := compileExpectedOutputs(n.ExpectOutputs)
	return err
}
func sourceContextSnapshot(s inlineRecipeSourceContext) recipe.RecipeSourceSnapshot {
	if s.GitRepositoryURL != "" {
		selector := fmt.Sprintf("git+%s//%s@%s", s.GitRepositoryURL, s.GitRecipePath, s.GitRef)
		return recipe.RecipeSourceSnapshot{SourceKind: string(RecipeSourceKindGit), ResolvedSelector: selector, ResolvedCommit: s.GitRef}
	}
	return recipe.RecipeSourceSnapshot{SourceKind: recipeSourceKindLocalFile, ResolvedSelector: s.LocalFile}
}
func selectLate(ctx context.Context, n *recipe.NodeInclude, parent *template.ResolutionContext, invocation string) (lateSelection, error) {
	value, err := parent.ResolveValueWithContext(ctx, n.Include.Recipe, template.ModeInterpolation)
	if err != nil {
		return lateSelection{}, fmt.Errorf("late include selector: %w", err)
	}
	selector, ok := value.(string)
	if !ok {
		return lateSelection{}, fmt.Errorf("late include selector must evaluate to a string")
	}
	p, err := parseGitRecipeSelector(selector)
	if err != nil {
		return lateSelection{}, fmt.Errorf("late include requires a full-commit Git selector: %w", err)
	}
	if !isFullGitHash(p.Ref) {
		return lateSelection{}, fmt.Errorf("late include requires a full 40-character hexadecimal commit")
	}
	repo, err := extops.NormalizeGitRepositorySourceForSelector(p.RepositoryURL)
	if err != nil {
		return lateSelection{}, err
	}
	if n.Internal == nil || n.Internal.Late == nil {
		return lateSelection{}, fmt.Errorf("late include has no declaring-source metadata; resolve inline recipes before execution")
	}
	remote, err := url.Parse(repo)
	if err != nil || remote.Scheme == "file" || remote.Host == "" {
		return lateSelection{}, fmt.Errorf("late include requires a remote Git repository")
	}
	declaration := *n.Internal.Late
	identity := permissions.RepositoryIdentity(repo)
	s := lateSelection{Invocation: invocation, Selector: selector, Repository: repo, Path: p.RecipePath, Commit: strings.ToLower(p.Ref), Declaration: declaration,
		PermissionBoundary: identity == "" || identity != declaration.DeclaringRepository, TrustedSource: identity != "" && identity == declaration.RootRepository}
	// Check ordinary include delegation before any target read. Caller input
	// authority and enclosing ceilings cannot be replaced by selected code.
	if _, err = parent.Permissions.EnterInclude(n.Perms, s.PermissionBoundary, s.TrustedSource); err != nil {
		return lateSelection{}, err
	}
	return s, nil
}

type lateSelectWorker struct{}

func (lateSelectWorker) Name() string { return lateSelectTaskType }
func (w lateSelectWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (lateSelectWorker) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	var s lateSelection
	raw, err := in.GetData()
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return nil, jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, "c2j.internal_task_input", nil, nil)
	}
	metricsruntime.Execution(ctx)
	if err = validateLateSelection(s); err != nil {
		return nil, jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_selection", nil, nil)
	}
	return jobdb.NewTaskData(s)
}
func validateLateSelection(s lateSelection) error {
	p, err := parseGitRecipeSelector(s.resolution().ResolvedSelector)
	if err != nil || s.Invocation == "" || !isFullGitHash(s.Commit) || strings.ToLower(s.Commit) != s.Commit || p.RecipePath != s.Path {
		return fmt.Errorf("invalid recorded late include selection")
	}
	authored, err := parseGitRecipeSelector(s.Selector)
	if err != nil {
		return fmt.Errorf("invalid recorded late include selector: %w", err)
	}
	repository, err := extops.NormalizeGitRepositorySourceForSelector(authored.RepositoryURL)
	identity := permissions.RepositoryIdentity(s.Repository)
	if err != nil || repository != s.Repository || authored.RecipePath != s.Path || strings.ToLower(authored.Ref) != s.Commit ||
		s.PermissionBoundary != (identity == "" || identity != s.Declaration.DeclaringRepository) ||
		s.TrustedSource != (identity != "" && identity == s.Declaration.RootRepository) {
		return fmt.Errorf("inconsistent recorded late include selection")
	}
	return nil
}

type lateCaptureWorker struct{ resolver RecipeSourceResolver }

func (lateCaptureWorker) Name() string { return lateCaptureTaskType }
func (w lateCaptureWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w lateCaptureWorker) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	if permissions.FromContext(ctx) == nil {
		return nil, fmt.Errorf("late capture requires a guarded task worker")
	}
	var req lateCaptureInput
	raw, err := in.GetData()
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &req); err != nil {
		return nil, jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, "c2j.internal_task_input", nil, nil)
	}
	if err = validateLateSelection(req.Selection); err != nil {
		return nil, jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_selection", nil, nil)
	}
	if req.Permissions == nil {
		return nil, fmt.Errorf("late capture requires caller authority")
	}
	effective, err := req.Permissions.EnterInclude(req.CallsitePerms, req.Selection.PermissionBoundary, req.Selection.TrustedSource)
	if err != nil {
		return nil, err
	}
	resolver := w.resolver
	if resolver == nil {
		resolver = NewRecipeSourceResolver(RecipeSourceResolverOptions{})
	}
	metricsruntime.Execution(ctx)
	resolution := req.Selection.resolution()
	// Bootstrap reads run with caller authority. Callee ceilings apply to the
	// selected definition's imports and all nested preparation, after this read.
	rec, err := loadRootRecipe(ctx, resolver, req.ProjectID, resolution)
	if err != nil {
		return nil, err
	}
	if effective.Limit != nil {
		ctx = gitprofile.WithSecretLimit(ctx, effective.Limit.Secrets.Resolve)
	}
	root := req.Selection.Declaration.RootRepository
	recipeMetadataPtr(&rec).Internal = &recipe.NodeInternalMetadata{RootRepository: &root}
	expanded, err := ResolveInlineRecipes(ctx, rec, InlineResolutionOptions{ProjectID: req.ProjectID, Resolver: resolver, RootSource: &resolution, lateAuthority: &effective})
	if err != nil {
		return nil, err
	}
	if _, _, _, err = includedRecipeRootNode(expanded.Recipe); err != nil {
		return nil, err
	}
	if err = bindHookIncludeSources(expanded.Recipe); err != nil {
		return nil, err
	}
	var selectors []string
	for _, imp := range expanded.Recipe.GetMetadata().Extensions.Functions {
		selectors = append(selectors, imp.Selector)
	}
	functions, err := resolveSelectors(ctx, selectors, extops.ResolveOptions{RepositorySource: req.Selection.Repository, RepositoryRef: req.Selection.Commit})
	if err != nil {
		return nil, err
	}

	snapshot := recipeSourceSnapshot(resolution)
	recipeMetadataPtr(&expanded.Recipe).Internal.DefinitionSource = &snapshot
	captured, err := marshalRecipeYAML(expanded.Recipe)
	if err != nil {
		return nil, err
	}
	result := lateCapture{Selection: req.Selection, RecipeYAMLArtifact: ResolvedRecipeYAMLArtifactName, ContentSHA256: sha256Hex(captured), Functions: functions}
	return jobdb.NewTaskData(result, jobdb.NewArtifactFromBytes(result.RecipeYAMLArtifact, captured))
}

func prepareLateInclude(ctx workflow.Context, parent *template.ResolutionContext, n *recipe.NodeInclude) (recipe.Node, error) {
	if err := validateLateInclude(n, ""); err != nil {
		return recipe.Node{}, err
	}
	if parent.Options.Mode == template.ModeValidate {
		// Check the authored expression without requiring a valid target value or
		// knowing its input schema. Normal wrapper validation checks other metadata.
		if _, err := parent.ResolveValueWithMode(n.Include.Recipe, template.ModeInterpolation); err != nil {
			return recipe.Node{}, err
		}
		meta := n.NodeMetadata
		meta.ExpectOutputs = nil
		return recipe.Node{NodeImpl: &recipe.NodeSequence{NodeMetadata: meta, SequenceData: recipe.SequenceData{Outputs: hookOutputPlaceholder(n)}}}, nil
	}
	invocation := parent.NextWorkspaceID("late:" + n.ID)
	command, err := json.Marshal(struct {
		Invocation string
		Call       *recipe.NodeInclude
	}{invocation, n})
	if err != nil {
		return recipe.Node{}, err
	}
	selected, err := ctx.JobContext.DoTask(jobworkflow.TaskCall{Site: "late:" + invocation + ":select", TaskType: lateSelectTaskType, ContractVersion: 1, Command: command}, func(build context.Context) (jobdb.TaskData, error) {
		if err := build.Err(); err != nil {
			return nil, err
		}
		s, err := selectLate(build, n, parent, invocation)
		if err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(s)
	})
	if err != nil {
		return recipe.Node{}, err
	}
	var selection lateSelection
	raw, err := selected.GetData()
	if err != nil {
		return recipe.Node{}, err
	}
	if err = coretask.DecodeInternal(raw, &selection); err != nil {
		return recipe.Node{}, err
	}
	if err = validateLateSelection(selection); err != nil {
		return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(err, jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_selection", nil, nil)
	}
	if selection.Invocation != invocation || n.Internal == nil || n.Internal.Late == nil || !reflect.DeepEqual(selection.Declaration, *n.Internal.Late) {
		return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("recorded late selection disagrees with declaring callsite"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_selection", nil, nil)
	}
	// Live authority/profile are only needed for unfinished capture work.
	captured, err := ctx.JobContext.DoTask(jobworkflow.TaskCall{Site: "late:" + invocation + ":capture", TaskType: lateCaptureTaskType, ContractVersion: 1, Command: raw}, func(build context.Context) (jobdb.TaskData, error) {
		if err := build.Err(); err != nil {
			return nil, err
		}
		view := parent.ExecutionView()
		if err := view.SelectGitProfile(n.GitProfile, ""); err != nil {
			return nil, err
		}
		return jobdb.NewTaskData(lateCaptureInput{Selection: selection, Permissions: &view.Permissions, GitProfile: view.GitProfile, CallsitePerms: n.Perms, ProjectID: ctx.GetJobKey().TenantId})
	})
	if err != nil {
		return recipe.Node{}, err
	}
	var result lateCapture
	raw, err = captured.GetData()
	if err != nil {
		return recipe.Node{}, err
	}
	if err = coretask.DecodeInternal(raw, &result); err != nil {
		return recipe.Node{}, err
	}
	if !reflect.DeepEqual(result.Selection, selection) || result.RecipeYAMLArtifact != ResolvedRecipeYAMLArtifactName {
		return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("late capture disagrees with accepted selection"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_capture", nil, nil)
	}
	artifact, err := findResolvedRecipeYAMLArtifact(captured, result.RecipeYAMLArtifact)
	if err != nil {
		return recipe.Node{}, err
	}
	bytes, err := readResolvedRecipeYAML(ctx.JobContext.Context(), result.RecipeYAMLArtifact, "", artifact)
	if err != nil {
		return recipe.Node{}, err
	}
	if sha256Hex(bytes) != result.ContentSHA256 {
		return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("late definition artifact digest mismatch"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_capture", nil, nil)
	}
	rec, err := recipe.LoadInternalRecipeFromString(bytes)
	if err != nil {
		return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("invalid captured late definition: %w", err), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_capture", nil, nil)
	}
	if err := recipe.ValidateJobSemantics(*rec, parent.TaskExecutionContext().JobSemanticsVersion); err != nil {
		return recipe.Node{}, err
	}
	if m := rec.GetMetadata().Internal; m == nil || m.RootRepository == nil || *m.RootRepository != selection.Declaration.RootRepository || m.DefinitionSource == nil || !reflect.DeepEqual(*m.DefinitionSource, recipeSourceSnapshot(selection.resolution())) {
		return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("captured late definition has inconsistent original root"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_capture", nil, nil)
	}
	loaded := loadedInlineRecipe{Recipe: *rec, Raw: bytes, Snapshot: recipeSourceSnapshot(selection.resolution()), TrustedSource: selection.TrustedSource, PermissionBoundary: selection.PermissionBoundary}
	wrapper, err := inlineWrapperNode(n, *rec, loaded, n.Internal.Late.CallsitePath, "")
	if err != nil {
		return recipe.Node{}, err
	}
	seq := wrapper.NodeImpl.(*recipe.NodeSequence)
	seq.When = recipecel.CELExpr{}
	bound, err := parent.ResolveCompositeInputsWithContext(ctx.JobContext.Context(), n.Inputs)
	if err != nil {
		return recipe.Node{}, err
	}
	bound, err = prepareCompositeInputs(seq.NodeMetadata, bound)
	if err != nil {
		return recipe.Node{}, fmt.Errorf("late include inputs do not match selected schema: %w", err)
	}
	seq.Internal.BoundInputs = bound
	seq.Internal.HookSourceRepo = selection.Repository
	seq.Internal.HookSourceRef = selection.Commit
	if len(rec.GetMetadata().Extensions.Functions) > 0 {
		if len(rec.GetMetadata().Internal.FunctionPackages) == 0 {
			return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("captured late CEL packages are missing"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_capture", nil, nil)
		}
		for _, imp := range rec.GetMetadata().Extensions.Functions {
			resolved := result.Functions.ResolvedSelectors[imp.Selector]
			key, ok, err := selectorRepoRefKey(imp.Selector, selection.Repository, selection.Commit)
			if err != nil || !ok || resolved != imp.Selector || !isFullGitHash(result.Functions.ResolvedGitRefs[key]) {
				return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("captured late CEL resolution metadata is missing or inconsistent"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_capture", nil, nil)
			}
			for _, pkg := range rec.GetMetadata().Internal.FunctionPackages {
				if pkg.Selector == imp.Selector && (pkg.ResolvedSelector != resolved || pkg.ResolvedCommit != result.Functions.ResolvedGitRefs[key]) {
					return recipe.Node{}, jobdb.ClassifyExecutionInfrastructure(fmt.Errorf("captured late CEL package disagrees with resolution metadata"), jobdb.ExecutionInfrastructureDurableDecode, "c2j.late_capture", nil, nil)
				}
			}
		}
		exec := parent.TaskExecutionContext().JobContext()
		exec.RecipeSource.Repo = selection.Repository
		exec.RecipeSource.Ref = selection.Commit
		seq.Internal.CELProvider, err = recipeCELOptionsProvider(ctx.JobContext.Context(), *rec, exec, parent.GetGitCommitContext(), ExecutionOptions{ResolvedSelectors: result.Functions.ResolvedSelectors, ResolvedGitRefs: result.Functions.ResolvedGitRefs}, parent.Options.CELOptionsProvider)
		if err != nil {
			return recipe.Node{}, err
		}
	}
	if err = ctx.JobContext.Context().Err(); err != nil {
		return recipe.Node{}, err
	}
	return wrapper, nil
}

// The outer scope owns preparation, the total deadline and preparation failures.
// The ordinary wrapper owns body retries and keeps its ordinary include authority.
func (d DefaultRecipeExecutor) executeLateInclude(ctx workflow.Context, parent *template.ResolutionContext, n *recipe.NodeInclude) error {
	outer := n.NodeMetadata
	outer.Retry = nil
	outer.GitProfile = nil
	if outer.Internal == nil {
		outer.Internal = &recipe.NodeInternalMetadata{}
	}
	preparing := true
	return executeNodeScopeWithCatchFilter(ctx, parent, outer, template.ScopeSequence, "", func(inner workflow.Context, state *template.ResolutionContext, _ recipe.NodeMetadata) error {
		wrapper, err := prepareLateInclude(inner, state, n)
		if err != nil {
			return err
		}
		inner, err = lateHookPolicy(inner, state, wrapper)
		if err != nil {
			return err
		}
		preparing = false
		seq := wrapper.NodeImpl.(*recipe.NodeSequence)
		seq.Timeout = 0
		inner.DeferScopeOutput = true
		return d.self().ExecuteNode(inner, state, &wrapper)
	}, latePreparationCatchFilter(&preparing))
}

func (d DefaultRecipeExecutor) runLateState(ctx workflow.Context, parent *template.ResolutionContext, name string, node recipe.State, transition template.TransitionData) (stateRunResult, error) {
	return d.runLateStateWithMachineCatch(ctx, parent, name, node, transition, nil)
}

func (d DefaultRecipeExecutor) runLateStateWithMachineCatch(ctx workflow.Context, parent *template.ResolutionContext, name string, node recipe.State, transition template.TransitionData, clauses []recipe.CatchClause) (stateRunResult, error) {
	call := *node.NodeImpl.(*recipe.NodeInclude)
	call.ID = name
	caller := parent.ExecutionView()
	caller.TemplateData.Transition = transition
	enabled, whenErr := caller.EvaluateCEL(call.When.String())
	var err error
	validateAll := caller.Options.Mode == template.ModeValidate && caller.Options.ValidationMode == string(ValidateAll)
	if whenErr == nil && !enabled && !validateAll {
		return stateRunResult{}, nil
	}
	outer := call.NodeMetadata
	outer.Retry = nil
	outer.GitProfile = nil
	preparing := true
	enter := func(ctx workflow.Context, caller *template.ResolutionContext) error {
		return executeNodeScopeWithCatchFilter(ctx, caller, outer, template.ScopeState, name, func(inner workflow.Context, state *template.ResolutionContext, _ recipe.NodeMetadata) error {
			if whenErr != nil {
				return whenErr
			}
			prepared, err := prepareLateInclude(inner, state, &call)
			if err != nil {
				return err
			}
			inner, err = lateHookPolicy(inner, state, prepared)
			if err != nil {
				return err
			}
			preparing = false
			prepared.NodeImpl.(*recipe.NodeSequence).Timeout = 0
			body := node
			body.Node = prepared
			inner.DeferScopeOutput = true
			result, err := d.runStateWithMachineCatch(inner, state, name, body, transition, clauses)
			if result.Route != nil {
				return &catchRouteError{Transition: *result.Route}
			}
			return err
		}, latePreparationCatchFilter(&preparing))
	}
	if len(clauses) == 0 {
		err = enter(ctx, caller)
	} else {
		// This scope owns one deadline across preparation, body retries and
		// retry waits. Its machine handlers apply only to preparation failures;
		// the body policy below preparation handles body failures before retry.
		policy := outer
		policy.ID = "late-state-entry:" + name
		policy.Catch = clauses
		outer.Timeout = 0
		machine := ctx
		machine.CatchStateName = name
		err = executeNodeScopeWithCatchFilter(machine, caller, policy, template.ScopeStateMachine, name, func(inner workflow.Context, entry *template.ResolutionContext, _ recipe.NodeMetadata) error {
			inner.DeferScopeOutput = true
			return enter(inner, entry)
		}, latePreparationCatchFilter(&preparing))
	}
	parent.AdoptExecution(caller)
	var route *catchRouteError
	if errors.As(err, &route) {
		return stateRunResult{Route: &route.Transition}, nil
	}
	return stateRunResult{}, err
}

// Body catches run at the retry boundary. Do not invoke them a second time
// when a deliberate fail propagates through the transparent preparation scope.
func latePreparationCatchFilter(preparing *bool) func(error) bool {
	return func(error) bool { return *preparing }
}

func lateHookPolicy(ctx workflow.Context, parent *template.ResolutionContext, wrapper recipe.Node) (workflow.Context, error) {
	seq := wrapper.NodeImpl.(*recipe.NodeSequence)
	rec := recipe.Recipe{RecipeImpl: &recipe.RecipeSequence{SequenceData: seq.SequenceData}}
	if ctx.HookRepository != "" || ctx.HookPolicy != nil || parent.Options.Mode == template.ModeValidate || !hasCellHooks(rec) {
		return ctx, nil
	}
	repository := parent.TaskExecutionContext().GitTask.BaseRepo
	if ctx.ExecutionHookPolicy {
		ctx.HookRepository = repository
		return ctx, nil
	}
	out, err := workflow.ValueCall(ctx.JobContext, "hook-policy", hookPolicyTaskType, jobdb.RunPolicy{}, map[string]string{"repository": repository})
	if err != nil {
		return ctx, err
	}
	raw, err := out.GetData()
	if err != nil {
		return ctx, err
	}
	err = coretask.DecodeInternal(raw, &ctx.HookPolicy)
	return ctx, err
}
