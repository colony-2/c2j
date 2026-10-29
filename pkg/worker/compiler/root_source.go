package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	gitpkg "github.com/colony-2/c2j/pkg/git"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"gopkg.in/yaml.v3"
)

const RootSourceResolutionTaskType = "recipe_root_source_resolve"

type RecipeSourceKind string

const (
	RecipeSourceKindArtifact  RecipeSourceKind = "artifact"
	RecipeSourceKindServerRef RecipeSourceKind = "serverRef"
	RecipeSourceKindGit       RecipeSourceKind = "git"
)

type RecipeSourceResolution struct {
	SourceKind        RecipeSourceKind `json:"source_kind"`
	SubmittedSelector string           `json:"submitted_selector"`
	ResolvedSelector  string           `json:"resolved_selector,omitempty"`
	ResolvedCommit    string           `json:"resolved_commit,omitempty"`
	ArtifactName      string           `json:"artifact_name,omitempty"`
	WasAlreadyPinned  bool             `json:"was_already_pinned"`
}

func (r RecipeSourceResolution) EffectiveSelector() string {
	if strings.TrimSpace(r.ResolvedSelector) != "" {
		return strings.TrimSpace(r.ResolvedSelector)
	}
	return strings.TrimSpace(r.SubmittedSelector)
}

func gitRefPinsFromRecipeSource(resolution RecipeSourceResolution) map[string]string {
	if resolution.SourceKind != RecipeSourceKindGit || strings.TrimSpace(resolution.ResolvedCommit) == "" {
		return nil
	}
	pins := map[string]string{}
	add := func(selector string) {
		selector = strings.TrimSpace(selector)
		if selector == "" || !isGitRecipeSelector(selector) {
			return
		}
		parsed, err := parseGitRecipeSelector(selector)
		if err != nil {
			return
		}
		pins[selectorcache.RepoRefKey(parsed.RepositoryURL, parsed.Ref)] = strings.TrimSpace(resolution.ResolvedCommit)
	}
	add(resolution.SubmittedSelector)
	add(resolution.ResolvedSelector)
	if len(pins) == 0 {
		return nil
	}
	return pins
}

// ResolvedRecipeYAMLArtifactName is a logical task-output name. JobDB owns its
// durable key, including when the artifact is inherited from an earlier job.
const ResolvedRecipeYAMLArtifactName = "_c2j/resolved-recipe.yaml"

type ResolvedRecipeSource struct {
	RecipeSourceResolution
	RecipeYAMLArtifact string `json:"recipe_yaml_artifact,omitempty"`
	// RecipeYAML is read compatibility for histories recorded before artifacts.
	RecipeYAML string `json:"recipe_yaml,omitempty"`

	recipeArtifact  jobdb.Artifact
	artifactContext bool
}

func (r ResolvedRecipeSource) HasRecipeYAML() bool {
	return r.recipeArtifact != nil || r.RecipeYAMLArtifact != "" || strings.TrimSpace(r.RecipeYAML) != ""
}

// RecipeYAMLBytes materializes source only when requested. Parsing metadata and
// enumerating artifacts must not open their content or put it back into history.
func (r ResolvedRecipeSource) RecipeYAMLBytes(ctx context.Context) ([]byte, error) {
	if r.RecipeYAMLArtifact != "" && !r.artifactContext {
		return nil, fmt.Errorf("resolved recipe artifact requires task data; use ParseResolvedRecipeSourceTaskData")
	}
	return readResolvedRecipeYAML(ctx, r.RecipeYAMLArtifact, r.RecipeYAML, r.recipeArtifact)
}

func (r ResolvedRecipeSource) LoadRecipe() (recipe.Recipe, error) {
	return r.LoadRecipeContext(context.Background())
}

func (r ResolvedRecipeSource) LoadRecipeContext(ctx context.Context) (recipe.Recipe, error) {
	raw, err := r.RecipeYAMLBytes(ctx)
	if err != nil {
		return recipe.Recipe{}, err
	}
	var rec recipe.Recipe
	if r.recipeArtifact != nil {
		var parsed *recipe.Recipe
		parsed, err = recipe.LoadInternalRecipeFromString(raw)
		if err == nil {
			rec = *parsed
		}
	} else {
		rec, err = loadEmbeddedRecipeYAML(raw)
	}
	if err != nil {
		return recipe.Recipe{}, fmt.Errorf("parse resolved recipe YAML: %w", err)
	}
	return rec, nil
}

// ParseResolvedRecipeSourceTaskData retains the artifact reader without fetching
// content. ParseResolvedRecipeSourceJSON is sufficient only for metadata views.
func ParseResolvedRecipeSourceTaskData(td jobdb.TaskData) (*ResolvedRecipeSource, error) {
	if td == nil {
		return nil, fmt.Errorf("resolved recipe source task data is required")
	}
	raw, err := td.GetData()
	if err != nil {
		return nil, err
	}
	source, err := ParseResolvedRecipeSourceJSON(raw)
	if err != nil {
		return nil, err
	}
	source.artifactContext = true
	source.recipeArtifact, err = findResolvedRecipeYAMLArtifact(td, source.RecipeYAMLArtifact)
	if err != nil {
		return nil, err
	}
	return source, nil
}

func findResolvedRecipeYAMLArtifact(td jobdb.TaskData, name string) (jobdb.Artifact, error) {
	if name == "" {
		name = ResolvedRecipeYAMLArtifactName
	}
	artifacts, err := td.GetArtifacts()
	if err != nil {
		return nil, fmt.Errorf("list resolved recipe artifacts: %w", err)
	}
	var found jobdb.Artifact
	for _, art := range artifacts {
		if art == nil || art.Name() != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("duplicate resolved recipe artifact %q", name)
		}
		found = art
	}
	return found, nil
}

func readResolvedRecipeYAML(ctx context.Context, name, inline string, art jobdb.Artifact) ([]byte, error) {
	var raw []byte
	if art != nil {
		var err error
		raw, err = art.Bytes(ctx)
		if err != nil {
			return nil, fmt.Errorf("read resolved recipe artifact %q: %w", art.Name(), err)
		}
	} else if strings.TrimSpace(inline) != "" {
		raw = []byte(inline)
	} else if name != "" {
		return nil, fmt.Errorf("resolved recipe artifact %q is missing", name)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("resolved recipe YAML is empty")
	}
	return raw, nil
}

func ParseResolvedRecipeSourceJSON(raw []byte) (*ResolvedRecipeSource, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("resolved recipe source payload is empty")
	}

	source := ResolvedRecipeSource{}
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, fmt.Errorf("decode resolved recipe source payload: %w", err)
	}
	if source.SourceKind == "" && strings.TrimSpace(source.SubmittedSelector) == "" && strings.TrimSpace(source.ResolvedSelector) == "" && strings.TrimSpace(source.ResolvedCommit) == "" && strings.TrimSpace(source.ArtifactName) == "" && !source.HasRecipeYAML() {
		return nil, fmt.Errorf("decode resolved recipe source payload: missing resolution fields")
	}
	return &source, nil
}

type RecipeSourceResolver interface {
	Resolve(ctx context.Context, projectID string, selector string) (RecipeSourceResolution, error)
	Load(ctx context.Context, projectID string, resolution RecipeSourceResolution) (recipe.Recipe, error)
}

type RecipeSourceYAMLLoader interface {
	LoadYAML(ctx context.Context, projectID string, resolution RecipeSourceResolution) ([]byte, error)
}

type RecipeRefResolver interface {
	ResolveRecipeRef(ctx context.Context, projectID string, selector string) (RecipeSourceResolution, error)
	LoadRecipeRef(ctx context.Context, projectID string, selector string) (*recipe.Recipe, error)
}

type RecipeRefYAMLLoader interface {
	LoadRecipeRefYAML(ctx context.Context, projectID string, selector string) ([]byte, error)
}

type RecipeSourceResolverOptions struct {
	// GitRepository is retained for API compatibility. Git selectors are
	// resolved through SelectorCache so extension and recipe sources share one
	// materialization path.
	GitRepository     gitpkg.Repository
	SelectorCache     *selectorcache.Cache
	RecipeRefResolver RecipeRefResolver
}

func NewRecipeSourceResolver(opts RecipeSourceResolverOptions) RecipeSourceResolver {
	sourceCache := opts.SelectorCache
	if sourceCache == nil {
		sourceCache = selectorcache.Default()
	}

	return &defaultRecipeSourceResolver{
		selectorCache:     sourceCache,
		recipeRefResolver: opts.RecipeRefResolver,
	}
}

func NewProviderBackedRecipeRefResolver(provider func(projectID string, recipeRef string) (*recipe.Recipe, error)) RecipeRefResolver {
	return providerBackedRecipeRefResolver{provider: provider}
}

func ValidateRecipeSelector(selector string) error {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return fmt.Errorf("recipe selector is required")
	}
	if isGitRecipeSelector(selector) {
		_, err := parseGitRecipeSelector(selector)
		return err
	}
	return nil
}

type defaultRecipeSourceResolver struct {
	selectorCache     *selectorcache.Cache
	recipeRefResolver RecipeRefResolver
}

func (r *defaultRecipeSourceResolver) Resolve(ctx context.Context, projectID string, selector string) (RecipeSourceResolution, error) {
	selector = strings.TrimSpace(selector)
	if err := ValidateRecipeSelector(selector); err != nil {
		return RecipeSourceResolution{}, err
	}

	if isGitRecipeSelector(selector) {
		return r.resolveGitSelector(ctx, selector)
	}
	if r.recipeRefResolver == nil {
		return RecipeSourceResolution{}, fmt.Errorf("recipe ref resolver not configured for %q", selector)
	}
	return r.recipeRefResolver.ResolveRecipeRef(ctx, projectID, selector)
}

func (r *defaultRecipeSourceResolver) Load(ctx context.Context, projectID string, resolution RecipeSourceResolution) (recipe.Recipe, error) {
	switch resolution.SourceKind {
	case RecipeSourceKindGit:
		return r.loadGitSelector(ctx, resolution)
	case RecipeSourceKindServerRef:
		if r.recipeRefResolver == nil {
			return recipe.Recipe{}, fmt.Errorf("recipe ref resolver not configured for %q", resolution.EffectiveSelector())
		}
		rec, err := r.recipeRefResolver.LoadRecipeRef(ctx, projectID, resolution.EffectiveSelector())
		if err != nil {
			return recipe.Recipe{}, err
		}
		if rec == nil {
			return recipe.Recipe{}, fmt.Errorf("recipe %q resolved to nil", resolution.EffectiveSelector())
		}
		return *rec, nil
	default:
		return recipe.Recipe{}, fmt.Errorf("unsupported recipe source kind %q", resolution.SourceKind)
	}
}

func (r *defaultRecipeSourceResolver) LoadYAML(ctx context.Context, projectID string, resolution RecipeSourceResolution) ([]byte, error) {
	switch resolution.SourceKind {
	case RecipeSourceKindGit:
		return r.loadGitSelectorYAML(ctx, resolution)
	case RecipeSourceKindServerRef:
		if r.recipeRefResolver == nil {
			return nil, fmt.Errorf("recipe ref resolver not configured for %q", resolution.EffectiveSelector())
		}
		if loader, ok := r.recipeRefResolver.(RecipeRefYAMLLoader); ok {
			return loader.LoadRecipeRefYAML(ctx, projectID, resolution.EffectiveSelector())
		}
		rec, err := r.recipeRefResolver.LoadRecipeRef(ctx, projectID, resolution.EffectiveSelector())
		if err != nil {
			return nil, err
		}
		if rec == nil {
			return nil, fmt.Errorf("recipe %q resolved to nil", resolution.EffectiveSelector())
		}
		return marshalRecipeYAML(*rec)
	default:
		return nil, fmt.Errorf("unsupported recipe source kind %q", resolution.SourceKind)
	}
}

func (r *defaultRecipeSourceResolver) resolveGitSelector(ctx context.Context, selector string) (RecipeSourceResolution, error) {
	parsed, err := parseGitRecipeSelector(selector)
	if err != nil {
		return RecipeSourceResolution{}, err
	}

	resolved, err := r.selectorCache.Resolve(ctx, selectorcache.ResolveRequest{
		RepositoryURL: parsed.RepositoryURL,
		Ref:           parsed.Ref,
	})
	if err != nil {
		return RecipeSourceResolution{}, fmt.Errorf("resolve git recipe selector %q: %w", selector, err)
	}

	return RecipeSourceResolution{
		SourceKind:        RecipeSourceKindGit,
		SubmittedSelector: selector,
		ResolvedSelector:  parsed.WithRef(resolved.Commit),
		ResolvedCommit:    resolved.Commit,
		WasAlreadyPinned:  isFullGitHash(parsed.Ref),
	}, nil
}

func (r *defaultRecipeSourceResolver) loadGitSelector(ctx context.Context, resolution RecipeSourceResolution) (recipe.Recipe, error) {
	yamlBytes, err := r.loadGitSelectorYAML(ctx, resolution)
	if err != nil {
		return recipe.Recipe{}, err
	}
	rec, err := recipe.LoadRecipeFromString(yamlBytes)
	if err != nil {
		return recipe.Recipe{}, fmt.Errorf("parse recipe from git selector %q: %w", resolution.EffectiveSelector(), err)
	}
	return *rec, nil
}

func (r *defaultRecipeSourceResolver) loadGitSelectorYAML(ctx context.Context, resolution RecipeSourceResolution) ([]byte, error) {
	selector := resolution.EffectiveSelector()
	parsed, err := parseGitRecipeSelector(selector)
	if err != nil {
		return nil, err
	}

	resolved, err := r.selectorCache.Resolve(ctx, selectorcache.ResolveRequest{
		RepositoryURL: parsed.RepositoryURL,
		Ref:           parsed.Ref,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve git recipe selector %q: %w", selector, err)
	}
	recipePath := filepath.Join(resolved.SourceDir, filepath.FromSlash(parsed.RecipePath))
	yamlBytes, err := os.ReadFile(recipePath)
	if err != nil {
		return nil, fmt.Errorf("load recipe %q from git selector %q: %w", parsed.RecipePath, selector, err)
	}

	return yamlBytes, nil
}

type providerBackedRecipeRefResolver struct {
	provider func(projectID string, recipeRef string) (*recipe.Recipe, error)
}

func (r providerBackedRecipeRefResolver) ResolveRecipeRef(_ context.Context, projectID string, selector string) (RecipeSourceResolution, error) {
	if r.provider == nil {
		return RecipeSourceResolution{}, fmt.Errorf("recipe provider not configured")
	}
	if _, err := r.provider(projectID, selector); err != nil {
		return RecipeSourceResolution{}, err
	}
	return RecipeSourceResolution{
		SourceKind:        RecipeSourceKindServerRef,
		SubmittedSelector: selector,
		ResolvedSelector:  selector,
		WasAlreadyPinned:  false,
	}, nil
}

func (r providerBackedRecipeRefResolver) LoadRecipeRef(_ context.Context, projectID string, selector string) (*recipe.Recipe, error) {
	if r.provider == nil {
		return nil, fmt.Errorf("recipe provider not configured")
	}
	return r.provider(projectID, selector)
}

type rootSourceResolutionTaskInput struct {
	Permissions *permissions.State   `json:"permissions,omitempty"`
	GitProfile  gitprofile.Selection `json:"git_profile,omitzero"`
	ProjectID   string               `json:"project_id"`
	Selector    string               `json:"selector"`
	LookupRepo  string               `json:"lookup_repo,omitempty"`
	LookupRef   string               `json:"lookup_ref,omitempty"`
}

type rootSourceResolutionTaskWorker struct {
	resolver RecipeSourceResolver
}

func newRootSourceResolutionTaskWorker(resolver RecipeSourceResolver) jobworkflow.TaskWorker {
	if resolver == nil {
		return nil
	}
	return rootSourceResolutionTaskWorker{resolver: resolver}
}

func (w rootSourceResolutionTaskWorker) Name() string {
	return RootSourceResolutionTaskType
}

func (w rootSourceResolutionTaskWorker) Run(taskCtx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(taskCtx.Context(), taskCtx, input)
}

func (w rootSourceResolutionTaskWorker) RunTask(local context.Context, taskCtx jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	if input == nil {
		return nil, fmt.Errorf("recipe source resolution input is required")
	}
	ctx, cancel := context.WithCancel(local)
	defer cancel()

	raw, err := input.GetData()
	if err != nil {
		return nil, err
	}
	req := rootSourceResolutionTaskInput{}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("decode recipe source resolution input: %w", err)
	}

	if w.resolver == nil {
		return nil, fmt.Errorf("recipe source resolver not configured")
	}
	submittedSelector := strings.TrimSpace(req.Selector)
	effectiveSelector, err := resolveRecipeSelectorForLookup(submittedSelector, strings.TrimSpace(req.LookupRepo), strings.TrimSpace(req.LookupRef))
	if err != nil {
		return nil, err
	}

	resolution, err := w.resolver.Resolve(ctx, strings.TrimSpace(req.ProjectID), effectiveSelector)
	if err != nil {
		return nil, err
	}
	rec, err := loadRootRecipe(ctx, w.resolver, strings.TrimSpace(req.ProjectID), resolution)
	if fallback := conventionalRecipeFallback(submittedSelector); fallback != "" && isGitRecipeSelector(effectiveSelector) && errors.Is(err, os.ErrNotExist) {
		// Only an absent root recipe triggers fallback. Resolve failures (bad
		// refs, authentication, network) and invalid recipes remain errors.
		resolution, err = w.resolver.Resolve(ctx, strings.TrimSpace(req.ProjectID), fallback)
		if err != nil {
			return nil, err
		}
		rec, err = loadRootRecipe(ctx, w.resolver, strings.TrimSpace(req.ProjectID), resolution)
	}
	if err != nil {
		return nil, err
	}
	if submittedSelector != "" {
		resolution.SubmittedSelector = submittedSelector
	}

	expanded, err := ResolveInlineRecipes(ctx, rec, InlineResolutionOptions{
		ProjectID:  strings.TrimSpace(req.ProjectID),
		Resolver:   w.resolver,
		RootSource: &resolution,
	})
	if err != nil {
		return nil, err
	}
	yamlBytes, err := marshalRecipeYAML(expanded.Recipe)
	if err != nil {
		return nil, err
	}

	source := ResolvedRecipeSource{
		RecipeSourceResolution: resolution,
		RecipeYAMLArtifact:     ResolvedRecipeYAMLArtifactName,
	}
	return jobdb.NewTaskData(source, jobdb.NewArtifactFromBytes(ResolvedRecipeYAMLArtifactName, yamlBytes))
}

func loadRootRecipe(ctx context.Context, resolver RecipeSourceResolver, projectID string, resolution RecipeSourceResolution) (recipe.Recipe, error) {
	if loader, ok := resolver.(RecipeSourceYAMLLoader); ok {
		raw, err := loader.LoadYAML(ctx, projectID, resolution)
		if err != nil {
			return recipe.Recipe{}, err
		}
		parsed, err := recipe.LoadRecipeFromString(raw)
		if err != nil {
			return recipe.Recipe{}, err
		}
		return *parsed, nil
	}
	return resolver.Load(ctx, projectID, resolution)
}

func resolveRecipeSelectorForLookup(selector string, lookupRepo string, lookupRef string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || isGitRecipeSelector(selector) {
		return selector, nil
	}
	if strings.TrimSpace(lookupRepo) == "" {
		return selector, nil
	}
	return BuildCellRecipeSelector(lookupRepo, selector, lookupRef)
}

func loadEmbeddedRecipeYAML(raw []byte) (recipe.Recipe, error) {
	parsed, err := recipe.LoadInternalRecipeFromString(raw)
	if err == nil {
		return *parsed, nil
	}

	var rec recipe.Recipe
	if err2 := yaml.Unmarshal(raw, &rec); err2 == nil && rec.RecipeImpl != nil {
		return rec, nil
	}

	var rawMap map[string]any
	if err2 := yaml.Unmarshal(raw, &rawMap); err2 == nil {
		if rawMap["sequence"] == nil && rawMap["state"] == nil && rawMap["op"] == nil && rawMap["child_group"] == nil {
			var seq recipe.RecipeSequence
			if err3 := yaml.Unmarshal(raw, &seq); err3 == nil {
				return recipe.Recipe{RecipeImpl: &seq}, nil
			}
		}
	}
	return recipe.Recipe{}, fmt.Errorf("parse embedded recipe YAML: %w", err)
}

func recipeMetadataPtr(rec *recipe.Recipe) *recipe.RecipeMetadata {
	switch typed := rec.RecipeImpl.(type) {
	case *recipe.RecipeOp:
		return &typed.RecipeMetadata
	case *recipe.RecipeSequence:
		return &typed.RecipeMetadata
	case *recipe.RecipeState:
		return &typed.RecipeMetadata
	case *recipe.RecipeChildGroup:
		return &typed.RecipeMetadata
	default:
		return nil
	}
}

type gitRecipeSelector struct {
	Raw           string
	RepositoryURL string
	RecipePath    string
	Ref           string
}

func (s gitRecipeSelector) WithRef(ref string) string {
	return fmt.Sprintf("git+%s//%s@%s", s.RepositoryURL, s.RecipePath, ref)
}

func isGitRecipeSelector(selector string) bool {
	return strings.HasPrefix(strings.TrimSpace(selector), "git+")
}

func parseGitRecipeSelector(selector string) (gitRecipeSelector, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector is required")
	}
	if !isGitRecipeSelector(selector) {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector must start with git+")
	}

	atIdx := strings.LastIndex(selector, "@")
	if atIdx <= 0 || atIdx == len(selector)-1 {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector %q must include a non-empty @<ref> suffix", selector)
	}

	beforeRef := selector[:atIdx]
	ref := strings.TrimSpace(selector[atIdx+1:])
	if ref == "" {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector %q has an empty ref", selector)
	}

	delimIdx := strings.LastIndex(beforeRef, "//")
	if delimIdx <= 0 || delimIdx >= len(beforeRef)-2 {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector %q must include //<repo-relative-path>", selector)
	}

	repositoryURL := beforeRef[:delimIdx]
	recipePath := beforeRef[delimIdx+2:]
	if strings.TrimSpace(recipePath) == "" {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector %q has an empty recipe path", selector)
	}
	if strings.Contains(recipePath, "\\") {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector %q must use forward slashes in recipe paths", selector)
	}

	normalizedPath := path.Clean(recipePath)
	if normalizedPath == "." || normalizedPath == ".." || strings.HasPrefix(normalizedPath, "../") || strings.HasPrefix(normalizedPath, "/") {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector %q has an invalid recipe path %q", selector, recipePath)
	}
	if strings.Contains(normalizedPath, "/./") || strings.Contains(normalizedPath, "/../") {
		return gitRecipeSelector{}, fmt.Errorf("git recipe selector %q has an invalid recipe path %q", selector, recipePath)
	}

	parsedURL, err := url.Parse(strings.TrimPrefix(repositoryURL, "git+"))
	if err != nil {
		return gitRecipeSelector{}, fmt.Errorf("parse git repository URL %q: %w", repositoryURL, err)
	}
	switch parsedURL.Scheme {
	case "file", "ssh", "http", "https":
	default:
		return gitRecipeSelector{}, fmt.Errorf("unsupported git recipe selector scheme %q", parsedURL.Scheme)
	}

	return gitRecipeSelector{
		Raw:           selector,
		RepositoryURL: strings.TrimPrefix(repositoryURL, "git+"),
		RecipePath:    normalizedPath,
		Ref:           ref,
	}, nil
}

func isFullGitHash(ref string) bool {
	ref = strings.TrimSpace(ref)
	if len(ref) != 40 {
		return false
	}
	for _, ch := range ref {
		switch {
		case ch >= '0' && ch <= '9':
		case ch >= 'a' && ch <= 'f':
		case ch >= 'A' && ch <= 'F':
		default:
			return false
		}
	}
	return true
}

func marshalRecipeYAML(rec recipe.Recipe) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(&rec)
	if err != nil {
		return nil, fmt.Errorf("marshal resolved recipe YAML: %w", err)
	}
	return yamlBytes, nil
}
