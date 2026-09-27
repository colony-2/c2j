package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type conventionResolver struct {
	RecipeSourceResolver
	resolve   func(string) (RecipeSourceResolution, error)
	load      func(RecipeSourceResolution) ([]byte, error)
	selectors []string
}

func (r *conventionResolver) Resolve(_ context.Context, _ string, selector string) (RecipeSourceResolution, error) {
	r.selectors = append(r.selectors, selector)
	return r.resolve(selector)
}

func (r *conventionResolver) LoadYAML(_ context.Context, _ string, res RecipeSourceResolution) ([]byte, error) {
	return r.load(res)
}

func TestConventionalRecipeFallbackRules(t *testing.T) {
	for _, tt := range []struct {
		name, selector                 string
		resolveErr, loadErr            error
		invalidYAML, fallback, wantErr bool
		fallbackMissing                bool
	}{
		{name: "build missing", selector: "build", loadErr: os.ErrNotExist, fallback: true},
		{name: "evolve missing", selector: "evolve", loadErr: fmt.Errorf("missing: %w", os.ErrNotExist), fallback: true},
		{name: "target override", selector: "build"},
		{name: "evolve override", selector: "evolve"},
		{name: "custom missing", selector: "custom", loadErr: os.ErrNotExist, wantErr: true},
		{name: "explicit selector", selector: "git+https://example.test/target.git//.c2j/recipes/build.yaml@main", loadErr: os.ErrNotExist, wantErr: true},
		{name: "permission", selector: "build", loadErr: os.ErrPermission, wantErr: true},
		{name: "network", selector: "evolve", resolveErr: fmt.Errorf("network failure"), wantErr: true},
		{name: "resolve not found", selector: "build", resolveErr: os.ErrNotExist, wantErr: true},
		{name: "invalid recipe", selector: "build", invalidYAML: true, wantErr: true},
		{name: "hosted recipe not published", selector: "build", loadErr: os.ErrNotExist, fallback: true, fallbackMissing: true, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &conventionResolver{}
			resolver.resolve = func(selector string) (RecipeSourceResolution, error) {
				return RecipeSourceResolution{SourceKind: RecipeSourceKindGit, SubmittedSelector: selector, ResolvedSelector: selector}, tt.resolveErr
			}
			resolver.load = func(res RecipeSourceResolution) ([]byte, error) {
				if tt.fallbackMissing {
					return nil, os.ErrNotExist
				}
				if !strings.Contains(res.EffectiveSelector(), "github.com/colony-2/recipes.git") {
					if tt.loadErr != nil {
						return nil, tt.loadErr
					}
					if tt.invalidYAML {
						return []byte("sequence: ["), nil
					}
				}
				return []byte("id: resolved\nversion: '1'\nsequence: []\n"), nil
			}
			input, err := jobdb.NewTaskData(rootSourceResolutionTaskInput{ProjectID: "tenant", Selector: tt.selector, LookupRepo: "https://example.test/target.git", LookupRef: "release"})
			require.NoError(t, err)
			output, err := newRootSourceResolutionTaskWorker(resolver).Run(jobworkflow.TaskContext{}, input)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				source, err := ParseResolvedRecipeSourceTaskData(output)
				require.NoError(t, err)
				require.Equal(t, tt.selector, source.SubmittedSelector)
			}
			if tt.fallback {
				require.Equal(t, []string{"git+https://example.test/target.git//.c2j/recipes/" + tt.selector + ".yaml@release", "git+https://github.com/colony-2/recipes.git//" + tt.selector + ".yaml@main"}, resolver.selectors)
			} else {
				require.Len(t, resolver.selectors, 1)
			}
		})
	}
}

func TestConventionalFallbackUsesExistingGitResolutionAndPreservesTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newRepo := func() string {
		dir := t.TempDir()
		require.NoError(t, runGit(dir, "git", "init", "-b", "main"))
		require.NoError(t, runGit(dir, "git", "config", "user.email", "conventions@example.test"))
		require.NoError(t, runGit(dir, "git", "config", "user.name", "Conventions"))
		require.NoError(t, runGit(dir, "git", "commit", "--allow-empty", "-m", "initial"))
		return dir
	}
	target, hosted := newRepo(), newRepo()
	writeRootSourceRecipe(t, filepath.Join(hosted, "phase.yaml"), "shared_phase")
	for _, kind := range []string{"build", "evolve"} {
		raw := "id: shared_" + kind + "\nversion: '1'\nsequence:\n  - id: phase\n    include: ./phase.yaml\n"
		require.NoError(t, os.WriteFile(filepath.Join(hosted, kind+".yaml"), []byte(raw), 0o644))
	}
	require.NoError(t, runGit(hosted, "git", "add", "."))
	require.NoError(t, runGit(hosted, "git", "commit", "-m", "shared recipes"))
	delegate := NewRecipeSourceResolver(RecipeSourceResolverOptions{SelectorCache: &selectorcache.Cache{Root: t.TempDir()}})
	resolver := &conventionResolver{RecipeSourceResolver: delegate}
	resolver.resolve = func(selector string) (RecipeSourceResolution, error) {
		selector = strings.Replace(selector, "https://github.com/colony-2/recipes.git", "file://"+hosted, 1)
		return delegate.Resolve(ctx, "tenant", selector)
	}
	resolver.load = func(res RecipeSourceResolution) ([]byte, error) {
		return delegate.(RecipeSourceYAMLLoader).LoadYAML(ctx, "tenant", res)
	}
	for _, kind := range []string{"build", "evolve"} {
		input, err := jobdb.NewTaskData(rootSourceResolutionTaskInput{ProjectID: "tenant", Selector: kind, LookupRepo: "file://" + target, LookupRef: "main"})
		require.NoError(t, err)
		output, err := newRootSourceResolutionTaskWorker(resolver).Run(jobworkflow.TaskContext{}, input)
		require.NoError(t, err)
		source, err := ParseResolvedRecipeSourceTaskData(output)
		require.NoError(t, err)
		require.Equal(t, gitHead(t, hosted), source.ResolvedCommit)
		require.Equal(t, "git+file://"+hosted+"//"+kind+".yaml@"+source.ResolvedCommit, source.ResolvedSelector)
		require.Equal(t, kind, source.SubmittedSelector)
		require.NotContains(t, source.RecipeYAML, "include:")
		rec, err := source.LoadRecipe()
		require.NoError(t, err)
		require.Equal(t, "shared_"+kind, rec.GetMetadata().ID)
		var runContext contextual.JobContext
		runContext.GitBase.BaseRepo = "file://" + target
		applyRootRecipeSource(&runContext, source.RecipeSourceResolution)
		require.Equal(t, "file://"+target, runContext.GitBase.BaseRepo)
		require.Equal(t, "file://"+hosted, runContext.RecipeSource.Repo)
	}
}
