package compiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/recipe"
)

const recipeSourceKindLocalFile = "localFile"

type InlineResolutionOptions struct {
	ProjectID  string
	RootFile   string
	WorkingDir string
	Resolver   RecipeSourceResolver
	RootSource *RecipeSourceResolution
}

type InlineResolutionResult struct {
	Recipe recipe.Recipe
}

func ResolveInlineRecipes(ctx context.Context, rec recipe.Recipe, opts InlineResolutionOptions) (InlineResolutionResult, error) {
	resolver := opts.Resolver
	if resolver == nil {
		resolver = NewRecipeSourceResolver(RecipeSourceResolverOptions{})
	}
	r := &inlineRecipeResolver{
		projectID: strings.TrimSpace(opts.ProjectID),
		resolver:  resolver,
		gitRefs:   make(map[string]string),
	}
	for key, value := range gitRefPinsFromRecipeSourceValue(opts.RootSource) {
		r.gitRefs[key] = value
	}
	rootSource, err := r.rootSourceContext(opts)
	if err != nil {
		return InlineResolutionResult{}, err
	}
	expanded, err := r.expandRecipe(ctx, rec, rootSource)
	if err != nil {
		return InlineResolutionResult{}, err
	}
	return InlineResolutionResult{Recipe: expanded}, nil
}

type inlineRecipeResolver struct {
	projectID  string
	resolver   RecipeSourceResolver
	gitRefs    map[string]string
	extensions recipe.ExtensionImports
}

type inlineRecipeSourceContext struct {
	LocalFile string
	LocalDir  string

	GitRepositoryURL string
	GitRef           string
	GitRecipePath    string
	GitRecipeDir     string
}

type loadedInlineRecipe struct {
	Recipe        recipe.Recipe
	Raw           []byte
	Snapshot      recipe.RecipeSourceSnapshot
	SourceContext inlineRecipeSourceContext
}

func (r *inlineRecipeResolver) rootSourceContext(opts InlineResolutionOptions) (inlineRecipeSourceContext, error) {
	if opts.RootSource != nil && opts.RootSource.SourceKind == RecipeSourceKindGit {
		parsed, err := parseGitRecipeSelector(opts.RootSource.EffectiveSelector())
		if err != nil {
			return inlineRecipeSourceContext{}, err
		}
		ref := strings.TrimSpace(opts.RootSource.ResolvedCommit)
		if ref == "" {
			ref = parsed.Ref
		}
		return inlineRecipeSourceContext{
			GitRepositoryURL: parsed.RepositoryURL,
			GitRef:           ref,
			GitRecipePath:    parsed.RecipePath,
			GitRecipeDir:     path.Dir(parsed.RecipePath),
		}, nil
	}

	rootFile := strings.TrimSpace(opts.RootFile)
	if rootFile == "" {
		return inlineRecipeSourceContext{}, nil
	}
	if !filepath.IsAbs(rootFile) {
		base := strings.TrimSpace(opts.WorkingDir)
		if base == "" {
			base = "."
		}
		abs, err := filepath.Abs(filepath.Join(base, rootFile))
		if err != nil {
			return inlineRecipeSourceContext{}, fmt.Errorf("resolve root recipe file %q: %w", rootFile, err)
		}
		rootFile = abs
	}
	return inlineRecipeSourceContext{
		LocalFile: rootFile,
		LocalDir:  filepath.Dir(rootFile),
	}, nil
}

func (r *inlineRecipeResolver) expandRecipe(ctx context.Context, rec recipe.Recipe, source inlineRecipeSourceContext) (recipe.Recipe, error) {
	switch node := rec.RecipeImpl.(type) {
	case *recipe.RecipeSequence:
		expanded, err := r.expandNodeList(ctx, node.Sequence, source, []string{"root"})
		if err != nil {
			return recipe.Recipe{}, err
		}
		out := *node
		out.Sequence = expanded
		mergeRecipeExtensions(&out.RecipeMetadata, r.extensions)
		return recipe.Recipe{RecipeImpl: &out}, nil
	case *recipe.RecipeState:
		expanded, err := r.expandStateMap(ctx, node.States, source, []string{"root"})
		if err != nil {
			return recipe.Recipe{}, err
		}
		out := *node
		out.States = expanded
		mergeRecipeExtensions(&out.RecipeMetadata, r.extensions)
		return recipe.Recipe{RecipeImpl: &out}, nil
	case *recipe.RecipeOp:
		return rec, nil
	case *recipe.RecipeChildGroup:
		return rec, nil
	default:
		return recipe.Recipe{}, fmt.Errorf("unsupported recipe type %T", rec.RecipeImpl)
	}
}

func (r *inlineRecipeResolver) expandNodeList(ctx context.Context, nodes []recipe.Node, source inlineRecipeSourceContext, pathParts []string) ([]recipe.Node, error) {
	out := make([]recipe.Node, 0, len(nodes))
	for i, node := range nodes {
		expanded, err := r.expandNode(ctx, node, source, append(pathParts, fmt.Sprintf("[%d]", i)), "")
		if err != nil {
			return nil, err
		}
		out = append(out, expanded)
	}
	return out, nil
}

func (r *inlineRecipeResolver) expandStateMap(ctx context.Context, states *recipe.StateMap, source inlineRecipeSourceContext, pathParts []string) (*recipe.StateMap, error) {
	if states == nil {
		return nil, nil
	}
	out := &recipe.StateMap{
		Initial: states.Initial,
		States:  make(map[string]recipe.State, len(states.States)),
	}
	names := make([]string, 0, len(states.States))
	for name := range states.States {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		state := states.States[name]
		expanded, err := r.expandNode(ctx, state.Node, source, append(pathParts, name), name)
		if err != nil {
			return nil, err
		}
		out.States[name] = recipe.State{
			Node:                expanded,
			SingleStateMetadata: state.SingleStateMetadata,
		}
	}
	return out, nil
}

func (r *inlineRecipeResolver) expandNode(ctx context.Context, node recipe.Node, source inlineRecipeSourceContext, pathParts []string, stateKey string) (recipe.Node, error) {
	switch n := node.NodeImpl.(type) {
	case *recipe.NodeInclude:
		return r.expandInclude(ctx, n, source, pathParts, stateKey)
	case *recipe.NodeSequence:
		expanded, err := r.expandNodeList(ctx, n.Sequence, source, pathParts)
		if err != nil {
			return recipe.Node{}, err
		}
		out := *n
		out.Sequence = expanded
		return recipe.Node{NodeImpl: &out}, nil
	case *recipe.NodeState:
		expanded, err := r.expandStateMap(ctx, n.States, source, pathParts)
		if err != nil {
			return recipe.Node{}, err
		}
		out := *n
		out.States = expanded
		return recipe.Node{NodeImpl: &out}, nil
	default:
		return node, nil
	}
}

func (r *inlineRecipeResolver) expandInclude(ctx context.Context, include *recipe.NodeInclude, parentSource inlineRecipeSourceContext, pathParts []string, stateKey string) (recipe.Node, error) {
	target := strings.TrimSpace(include.Include.Recipe)
	if target == "" {
		return recipe.Node{}, fmt.Errorf("include at %s must specify a recipe reference", strings.Join(pathParts, "/"))
	}

	loaded, err := r.loadIncludedRecipe(ctx, target, parentSource)
	if err != nil {
		return recipe.Node{}, fmt.Errorf("resolve include %q at %s: %w", target, strings.Join(pathParts, "/"), err)
	}
	expandedRecipe, err := r.expandRecipe(ctx, loaded.Recipe, loaded.SourceContext)
	if err != nil {
		return recipe.Node{}, fmt.Errorf("expand include %q at %s: %w", target, strings.Join(pathParts, "/"), err)
	}
	r.extensions.Functions = append(r.extensions.Functions, expandedRecipe.GetMetdata().Extensions.Functions...)

	wrapper, err := inlineWrapperNode(include, expandedRecipe, loaded, strings.Join(pathParts, "/"), stateKey)
	if err != nil {
		return recipe.Node{}, err
	}
	return wrapper, nil
}

func (r *inlineRecipeResolver) loadIncludedRecipe(ctx context.Context, target string, parentSource inlineRecipeSourceContext) (loadedInlineRecipe, error) {
	switch {
	case isGitRecipeSelector(target):
		return r.loadGitRecipe(ctx, target)
	case IsLocalRecipeFileReference(target):
		if parentSource.GitRepositoryURL != "" {
			gitPath := path.Clean(path.Join(parentSource.GitRecipeDir, filepath.ToSlash(target)))
			if gitPath == "." || strings.HasPrefix(gitPath, "../") || strings.HasPrefix(gitPath, "/") {
				return loadedInlineRecipe{}, fmt.Errorf("relative include %q escapes git recipe directory", target)
			}
			ref := strings.TrimSpace(parentSource.GitRef)
			if ref == "" {
				return loadedInlineRecipe{}, fmt.Errorf("relative git include %q has no resolved parent ref", target)
			}
			loaded, err := r.loadGitRecipe(ctx, fmt.Sprintf("git+%s//%s@%s", parentSource.GitRepositoryURL, gitPath, ref))
			if err != nil {
				return loadedInlineRecipe{}, err
			}
			loaded.Snapshot.SubmittedSelector = target
			return loaded, nil
		}
		if parentSource.LocalDir == "" {
			return loadedInlineRecipe{}, fmt.Errorf("relative include %q has no local recipe directory", target)
		}
		localPath := target
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(parentSource.LocalDir, target)
		}
		return loadLocalInlineRecipe(localPath, target)
	default:
		return r.loadResolvedRecipe(ctx, target)
	}
}

func loadLocalInlineRecipe(filename string, submitted string) (loadedInlineRecipe, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return loadedInlineRecipe{}, fmt.Errorf("resolve local recipe path %q: %w", filename, err)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return loadedInlineRecipe{}, fmt.Errorf("read local recipe %q: %w", abs, err)
	}
	rec, err := recipe.LoadRecipeFromString(raw)
	if err != nil {
		return loadedInlineRecipe{}, fmt.Errorf("parse local recipe %q: %w", abs, err)
	}
	return loadedInlineRecipe{
		Recipe: recValue(rec),
		Raw:    raw,
		Snapshot: recipe.RecipeSourceSnapshot{
			SourceKind:        recipeSourceKindLocalFile,
			SubmittedSelector: submitted,
			ResolvedSelector:  abs,
		},
		SourceContext: inlineRecipeSourceContext{
			LocalFile: abs,
			LocalDir:  filepath.Dir(abs),
		},
	}, nil
}

func (r *inlineRecipeResolver) loadGitRecipe(ctx context.Context, selector string) (loadedInlineRecipe, error) {
	resolution, err := r.resolveGitRecipe(ctx, selector)
	if err != nil {
		return loadedInlineRecipe{}, err
	}
	loaded, err := r.loadRecipeFromResolution(ctx, resolution)
	if err != nil {
		return loadedInlineRecipe{}, err
	}
	parsed, err := parseGitRecipeSelector(resolution.EffectiveSelector())
	if err != nil {
		return loadedInlineRecipe{}, err
	}
	ref := strings.TrimSpace(resolution.ResolvedCommit)
	if ref == "" {
		ref = parsed.Ref
	}
	loaded.SourceContext = inlineRecipeSourceContext{
		GitRepositoryURL: parsed.RepositoryURL,
		GitRef:           ref,
		GitRecipePath:    parsed.RecipePath,
		GitRecipeDir:     path.Dir(parsed.RecipePath),
	}
	return loaded, nil
}

func (r *inlineRecipeResolver) loadResolvedRecipe(ctx context.Context, selector string) (loadedInlineRecipe, error) {
	resolution, err := r.resolver.Resolve(ctx, r.projectID, selector)
	if err != nil {
		return loadedInlineRecipe{}, err
	}
	return r.loadRecipeFromResolution(ctx, resolution)
}

func (r *inlineRecipeResolver) resolveGitRecipe(ctx context.Context, selector string) (RecipeSourceResolution, error) {
	parsed, err := parseGitRecipeSelector(selector)
	if err != nil {
		return RecipeSourceResolution{}, err
	}
	cacheKey := selectorcache.RepoRefKey(parsed.RepositoryURL, parsed.Ref)
	if commit := r.gitRefs[cacheKey]; strings.TrimSpace(commit) != "" {
		return RecipeSourceResolution{
			SourceKind:        RecipeSourceKindGit,
			SubmittedSelector: selector,
			ResolvedSelector:  parsed.WithRef(commit),
			ResolvedCommit:    commit,
			WasAlreadyPinned:  isFullGitHash(parsed.Ref),
		}, nil
	}
	resolution, err := r.resolver.Resolve(ctx, r.projectID, selector)
	if err != nil {
		return RecipeSourceResolution{}, err
	}
	commit := strings.TrimSpace(resolution.ResolvedCommit)
	if commit == "" {
		resolved, parseErr := parseGitRecipeSelector(resolution.EffectiveSelector())
		if parseErr == nil && isFullGitHash(resolved.Ref) {
			commit = resolved.Ref
		}
	}
	if commit != "" {
		r.gitRefs[cacheKey] = commit
	}
	return resolution, nil
}

func gitRefPinsFromRecipeSourceValue(resolution *RecipeSourceResolution) map[string]string {
	if resolution == nil {
		return nil
	}
	return gitRefPinsFromRecipeSource(*resolution)
}

func (r *inlineRecipeResolver) loadRecipeFromResolution(ctx context.Context, resolution RecipeSourceResolution) (loadedInlineRecipe, error) {
	var raw []byte
	var rec recipe.Recipe
	if loader, ok := r.resolver.(RecipeSourceYAMLLoader); ok {
		yamlBytes, err := loader.LoadYAML(ctx, r.projectID, resolution)
		if err != nil {
			return loadedInlineRecipe{}, err
		}
		parsed, err := recipe.LoadRecipeFromString(yamlBytes)
		if err != nil {
			return loadedInlineRecipe{}, err
		}
		raw = yamlBytes
		rec = recValue(parsed)
	} else {
		loaded, err := r.resolver.Load(ctx, r.projectID, resolution)
		if err != nil {
			return loadedInlineRecipe{}, err
		}
		yamlBytes, err := marshalRecipeYAML(loaded)
		if err != nil {
			return loadedInlineRecipe{}, err
		}
		raw = yamlBytes
		rec = loaded
	}
	return loadedInlineRecipe{
		Recipe:   rec,
		Raw:      raw,
		Snapshot: recipeSourceSnapshot(resolution),
	}, nil
}

func inlineWrapperNode(include *recipe.NodeInclude, rec recipe.Recipe, loaded loadedInlineRecipe, callsitePath string, stateKey string) (recipe.Node, error) {
	inner, innerOutputs, meta, err := includedRecipeRootNode(rec)
	if err != nil {
		return recipe.Node{}, fmt.Errorf("include %q: %w", include.Include.Recipe, err)
	}

	wrapperID := strings.TrimSpace(include.ID)
	if strings.TrimSpace(stateKey) != "" {
		wrapperID = strings.TrimSpace(stateKey)
	}
	if wrapperID == "" {
		wrapperID = generatedIncludeID(meta.ID, include.Include.Recipe)
	}
	innerID := strings.TrimSpace(inner.GetMetadata().ID)
	if innerID == "" || innerID == wrapperID {
		innerID = "root"
		inner = withNodeID(inner, innerID)
	}

	wrapperMeta := include.NodeMetadata
	wrapperMeta.ID = wrapperID
	wrapperMeta.Internal = &recipe.NodeInternalMetadata{
		Inline: &recipe.InlineInclusionMetadata{
			CallsitePath:      callsitePath,
			RecipeID:          meta.ID,
			RecipeVersion:     meta.Version,
			Source:            loaded.Snapshot,
			ContentSHA256:     sha256Hex(loaded.Raw),
			ResolvedSelectors: nil,
		},
		CompositeInputSchema: cloneInputSchema(meta.InputSchema),
	}

	return recipe.Node{NodeImpl: &recipe.NodeSequence{
		NodeMetadata: wrapperMeta,
		SequenceData: recipe.SequenceData{
			Sequence: []recipe.Node{inner},
			Outputs:  passThroughOutputs(innerID, innerOutputs),
		},
	}}, nil
}

func includedRecipeRootNode(rec recipe.Recipe) (recipe.Node, map[string]interface{}, recipe.RecipeMetadata, error) {
	switch root := rec.RecipeImpl.(type) {
	case *recipe.RecipeSequence:
		meta := root.RecipeMetadata
		node := &recipe.NodeSequence{
			NodeMetadata: meta.NodeMetadata,
			SequenceData: root.SequenceData,
		}
		return recipe.Node{NodeImpl: node}, root.Outputs, meta, nil
	case *recipe.RecipeState:
		meta := root.RecipeMetadata
		node := &recipe.NodeState{
			NodeMetadata:     meta.NodeMetadata,
			StateMachineData: root.StateMachineData,
		}
		return recipe.Node{NodeImpl: node}, root.Outputs, meta, nil
	case *recipe.RecipeOp:
		return recipe.Node{}, nil, recipe.RecipeMetadata{}, fmt.Errorf("root op recipes cannot be included without an explicit output contract; wrap the op in a sequence recipe with outputs")
	case *recipe.RecipeChildGroup:
		return recipe.Node{}, nil, recipe.RecipeMetadata{}, fmt.Errorf("root child_group recipes cannot be included without an explicit output contract; wrap the child_group in a sequence recipe with outputs")
	default:
		return recipe.Node{}, nil, recipe.RecipeMetadata{}, fmt.Errorf("unsupported included recipe type %T", rec.RecipeImpl)
	}
}

func withNodeID(node recipe.Node, id string) recipe.Node {
	switch n := node.NodeImpl.(type) {
	case *recipe.NodeOp:
		out := *n
		out.ID = id
		return recipe.Node{NodeImpl: &out}
	case *recipe.NodeChildGroup:
		out := *n
		out.ID = id
		return recipe.Node{NodeImpl: &out}
	case *recipe.NodeSequence:
		out := *n
		out.ID = id
		return recipe.Node{NodeImpl: &out}
	case *recipe.NodeState:
		out := *n
		out.ID = id
		return recipe.Node{NodeImpl: &out}
	default:
		return node
	}
}

func passThroughOutputs(innerID string, outputs map[string]interface{}) map[string]interface{} {
	if len(outputs) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(outputs))
	for key := range outputs {
		out[key] = fmt.Sprintf("${{ sequence[%s].outputs[%s] }}", quoteCELString(innerID), quoteCELString(key))
	}
	return out
}

func quoteCELString(value string) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

func generatedIncludeID(recipeID string, ref string) string {
	source := strings.TrimSpace(recipeID)
	if source == "" {
		source = strings.TrimSpace(path.Base(filepath.ToSlash(ref)))
	}
	source = strings.TrimSuffix(source, filepath.Ext(source))
	if source == "" || source == "." || source == "/" {
		source = "recipe"
	}
	var b strings.Builder
	b.WriteString("include_")
	for _, ch := range source {
		switch {
		case ch >= 'a' && ch <= 'z':
			b.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			b.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		default:
			b.WriteRune('_')
		}
	}
	return strings.TrimRight(b.String(), "_")
}

func recValue(rec *recipe.Recipe) recipe.Recipe {
	if rec == nil {
		return recipe.Recipe{}
	}
	return *rec
}

func recipeSourceSnapshot(resolution RecipeSourceResolution) recipe.RecipeSourceSnapshot {
	return recipe.RecipeSourceSnapshot{
		SourceKind:        string(resolution.SourceKind),
		SubmittedSelector: resolution.SubmittedSelector,
		ResolvedSelector:  resolution.ResolvedSelector,
		ResolvedCommit:    resolution.ResolvedCommit,
		ArtifactName:      resolution.ArtifactName,
		WasAlreadyPinned:  resolution.WasAlreadyPinned,
	}
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func cloneInputSchema(in map[string]recipe.InputSchema) map[string]recipe.InputSchema {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]recipe.InputSchema, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mergeRecipeExtensions(meta *recipe.RecipeMetadata, additions recipe.ExtensionImports) {
	if meta == nil || len(additions.Functions) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(meta.Extensions.Functions)+len(additions.Functions))
	for _, fn := range meta.Extensions.Functions {
		seen[extensionImportKey(fn)] = struct{}{}
	}
	for _, fn := range additions.Functions {
		key := extensionImportKey(fn)
		if _, ok := seen[key]; ok {
			continue
		}
		meta.Extensions.Functions = append(meta.Extensions.Functions, fn)
		seen[key] = struct{}{}
	}
}

func extensionImportKey(fn recipe.ExtensionFunctionImport) string {
	raw, err := json.Marshal(fn)
	if err != nil {
		return fn.Selector
	}
	return string(raw)
}
