package compiler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestResolveInlineRecipesExpandsLocalIncludeAndExecutesWithInputDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	rootPath := filepath.Join(tmpDir, "root.yaml")
	phasePath := filepath.Join(tmpDir, "phase.yaml")

	require.NoError(t, os.WriteFile(phasePath, []byte(strings.TrimSpace(`
id: phase_recipe
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
  model:
    type: string
    default_value: tiny
inputs:
  prompt: "${{ inputs.prompt }}"
  model: "${{ inputs.model }}"
sequence: []
outputs:
  message: "${{ inputs.prompt }}"
  model: "${{ inputs.model }}"
`)+"\n"), 0o644))
	require.NoError(t, os.WriteFile(rootPath, []byte(strings.TrimSpace(`
id: root_recipe
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
inputs:
  prompt: "${{ inputs.prompt }}"
sequence:
  - id: phase
    include: ./phase.yaml
    inputs:
      prompt: "${{ inputs.prompt }}"
outputs:
  message: "${{ sequence.phase.outputs.message }}"
  model: "${{ sequence.phase.outputs.model }}"
`)+"\n"), 0o644))

	parsed, err := recipe.LoadRecipeFromString(mustReadFile(t, rootPath))
	require.NoError(t, err)

	expanded, err := ResolveInlineRecipes(context.Background(), *parsed, InlineResolutionOptions{RootFile: rootPath})
	require.NoError(t, err)
	requireNoIncludeNodes(t, expanded.Recipe)

	root := expanded.Recipe.RecipeImpl.(*recipe.RecipeSequence)
	require.Len(t, root.Sequence, 1)
	wrapper := root.Sequence[0].NodeImpl.(*recipe.NodeSequence)
	require.Equal(t, "phase", wrapper.ID)
	require.NotNil(t, wrapper.Internal)
	require.NotNil(t, wrapper.Internal.Inline)
	require.Equal(t, "phase_recipe", wrapper.Internal.Inline.RecipeID)
	require.Equal(t, recipeSourceKindLocalFile, wrapper.Internal.Inline.Source.SourceKind)
	require.Equal(t, "./phase.yaml", wrapper.Internal.Inline.Source.SubmittedSelector)
	require.Equal(t, phasePath, wrapper.Internal.Inline.Source.ResolvedSelector)
	require.NotEmpty(t, wrapper.Internal.Inline.ContentSHA256)
	require.Contains(t, wrapper.Internal.CompositeInputSchema, "model")

	jobCtx, gitCtx := GenerateTestContext()
	wfCtx := newWorkflowContext(&countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "inline-local"}})
	out, _, err := ExecuteRecipe(wfCtx, expanded.Recipe, map[string]interface{}{"prompt": "hello"}, jobCtx, gitCtx)
	require.NoError(t, err)
	require.Equal(t, map[string]interface{}{
		"message": "hello",
		"model":   "tiny",
	}, out)
}

func TestResolveInlineRecipesReusesOneCommitForSameGitSourceAndRef(t *testing.T) {
	const firstCommit = "1111111111111111111111111111111111111111"
	const secondCommit = "2222222222222222222222222222222222222222"

	resolver := &flippingGitResolver{
		commits: []string{firstCommit, secondCommit},
		files: map[string]string{
			"recipes/a.yaml": strings.TrimSpace(`
id: recipe_a
version: "1.0"
sequence: []
outputs:
  name: a
`) + "\n",
			"recipes/b.yaml": strings.TrimSpace(`
id: recipe_b
version: "1.0"
sequence: []
outputs:
  name: b
`) + "\n",
		},
	}

	root, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(`
id: root
version: "1.0"
sequence:
  - id: first
    include: git+https://example.com/acme/recipes.git//recipes/a.yaml@main
  - id: second
    include: git+https://example.com/acme/recipes.git//recipes/b.yaml@main
outputs:
  first: "${{ sequence.first.outputs.name }}"
  second: "${{ sequence.second.outputs.name }}"
`) + "\n"))
	require.NoError(t, err)

	expanded, err := ResolveInlineRecipes(context.Background(), *root, InlineResolutionOptions{Resolver: resolver})
	require.NoError(t, err)
	require.Equal(t, 1, resolver.resolveCalls, "same repo/ref should resolve once even when two recipe paths are included")
	requireNoIncludeNodes(t, expanded.Recipe)

	seq := expanded.Recipe.RecipeImpl.(*recipe.RecipeSequence)
	require.Len(t, seq.Sequence, 2)
	first := seq.Sequence[0].NodeImpl.(*recipe.NodeSequence).Internal.Inline.Source
	second := seq.Sequence[1].NodeImpl.(*recipe.NodeSequence).Internal.Inline.Source
	require.Equal(t, firstCommit, first.ResolvedCommit)
	require.Equal(t, firstCommit, second.ResolvedCommit)
	require.Contains(t, first.ResolvedSelector, "recipes/a.yaml@"+firstCommit)
	require.Contains(t, second.ResolvedSelector, "recipes/b.yaml@"+firstCommit)
	require.NotContains(t, first.ResolvedSelector, secondCommit)
	require.NotContains(t, second.ResolvedSelector, secondCommit)
}

func TestResolveInlineRecipesUsesRootRepoRefPinForExplicitSameRepoInclude(t *testing.T) {
	const rootCommit = "1111111111111111111111111111111111111111"
	const movedCommit = "2222222222222222222222222222222222222222"

	resolver := &flippingGitResolver{
		commits: []string{movedCommit},
		files: map[string]string{
			"recipes/a.yaml": strings.TrimSpace(`
id: recipe_a
version: "1.0"
sequence: []
outputs:
  name: a
`) + "\n",
		},
	}

	root, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(`
id: root
version: "1.0"
sequence:
  - id: first
    include: git+https://example.com/acme/recipes.git//recipes/a.yaml@main
outputs:
  first: "${{ sequence.first.outputs.name }}"
`) + "\n"))
	require.NoError(t, err)

	expanded, err := ResolveInlineRecipes(context.Background(), *root, InlineResolutionOptions{
		Resolver: resolver,
		RootSource: &RecipeSourceResolution{
			SourceKind:        RecipeSourceKindGit,
			SubmittedSelector: "git+https://example.com/acme/recipes.git//recipes/root.yaml@main",
			ResolvedSelector:  "git+https://example.com/acme/recipes.git//recipes/root.yaml@" + rootCommit,
			ResolvedCommit:    rootCommit,
		},
	})
	require.NoError(t, err)
	require.Equal(t, 0, resolver.resolveCalls, "same root repo/ref include should use the root source pin")
	requireNoIncludeNodes(t, expanded.Recipe)

	seq := expanded.Recipe.RecipeImpl.(*recipe.RecipeSequence)
	require.Len(t, seq.Sequence, 1)
	source := seq.Sequence[0].NodeImpl.(*recipe.NodeSequence).Internal.Inline.Source
	require.Equal(t, rootCommit, source.ResolvedCommit)
	require.Contains(t, source.ResolvedSelector, "recipes/a.yaml@"+rootCommit)
	require.NotContains(t, source.ResolvedSelector, movedCommit)
}

func TestInlineBoundaryStackPropagatesStructurally(t *testing.T) {
	root, err := template.NewRecipeResolutionContext(
		&contextual.GitCommitContext{},
		map[string]interface{}{},
		contextual.JobContext{},
	)
	require.NoError(t, err)

	meta := recipe.NodeMetadata{
		ID: "included",
		Internal: &recipe.NodeInternalMetadata{
			Inline: &recipe.InlineInclusionMetadata{
				CallsitePath:  "root/[0]",
				RecipeID:      "phase_recipe",
				RecipeVersion: "1.0",
				ContentSHA256: "abc123",
				Source: recipe.RecipeSourceSnapshot{
					SourceKind:       "git",
					ResolvedSelector: "git+https://example.com/acme/recipes.git//phase.yaml@1111111111111111111111111111111111111111",
					ResolvedCommit:   "1111111111111111111111111111111111111111",
				},
			},
		},
	}
	included, err := root.NewChildContext(template.ScopeSequence, meta, "", map[string]interface{}{})
	require.NoError(t, err)
	require.Len(t, included.TaskExecutionContext().InlineStack, 1)
	require.Equal(t, "included", included.TaskExecutionContext().InlineStack[0].BoundaryNodePath)

	opCtx, err := included.NewChildContext(template.ScopeOp, recipe.NodeMetadata{ID: "leaf"}, "leaf", nil)
	require.NoError(t, err)
	require.Len(t, opCtx.TaskExecutionContext().InlineStack, 1)
	require.Equal(t, "phase_recipe", opCtx.TaskExecutionContext().InlineStack[0].RecipeID)
	require.Equal(t, "included", opCtx.TaskExecutionContext().InlineStack[0].BoundaryNodePath)
}

type flippingGitResolver struct {
	commits      []string
	files        map[string]string
	resolveCalls int
}

func (r *flippingGitResolver) Resolve(_ context.Context, _ string, selector string) (RecipeSourceResolution, error) {
	parsed, err := parseGitRecipeSelector(selector)
	if err != nil {
		return RecipeSourceResolution{}, err
	}
	idx := r.resolveCalls
	if idx >= len(r.commits) {
		idx = len(r.commits) - 1
	}
	r.resolveCalls++
	commit := r.commits[idx]
	return RecipeSourceResolution{
		SourceKind:        RecipeSourceKindGit,
		SubmittedSelector: selector,
		ResolvedSelector:  parsed.WithRef(commit),
		ResolvedCommit:    commit,
		WasAlreadyPinned:  isFullGitHash(parsed.Ref),
	}, nil
}

func (r *flippingGitResolver) Load(_ context.Context, _ string, resolution RecipeSourceResolution) (recipe.Recipe, error) {
	raw, err := r.LoadYAML(context.Background(), "", resolution)
	if err != nil {
		return recipe.Recipe{}, err
	}
	rec, err := recipe.LoadRecipeFromString(raw)
	if err != nil {
		return recipe.Recipe{}, err
	}
	return *rec, nil
}

func (r *flippingGitResolver) LoadYAML(_ context.Context, _ string, resolution RecipeSourceResolution) ([]byte, error) {
	parsed, err := parseGitRecipeSelector(resolution.EffectiveSelector())
	if err != nil {
		return nil, err
	}
	raw, ok := r.files[parsed.RecipePath]
	if !ok {
		return nil, fmt.Errorf("missing fake recipe %q", parsed.RecipePath)
	}
	return []byte(raw), nil
}

func (r *flippingGitResolver) Logger() *slog.Logger {
	return slog.Default()
}

func requireNoIncludeNodes(t *testing.T, rec recipe.Recipe) {
	t.Helper()
	switch root := rec.RecipeImpl.(type) {
	case *recipe.RecipeSequence:
		requireNoIncludeNodeList(t, root.Sequence)
	case *recipe.RecipeState:
		requireNoIncludeStateMap(t, root.States)
	}
}

func requireNoIncludeNodeList(t *testing.T, nodes []recipe.Node) {
	t.Helper()
	for _, node := range nodes {
		switch n := node.NodeImpl.(type) {
		case *recipe.NodeInclude:
			t.Fatalf("unexpected unresolved include node: %#v", n)
		case *recipe.NodeSequence:
			requireNoIncludeNodeList(t, n.Sequence)
		case *recipe.NodeState:
			requireNoIncludeStateMap(t, n.States)
		}
	}
}

func requireNoIncludeStateMap(t *testing.T, states *recipe.StateMap) {
	t.Helper()
	if states == nil {
		return
	}
	for _, state := range states.States {
		requireNoIncludeNodeList(t, []recipe.Node{state.Node})
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}
