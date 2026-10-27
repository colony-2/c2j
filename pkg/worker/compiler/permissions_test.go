package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func TestIncludePermissionsUseRepositoryBoundaries(t *testing.T) {
	root := t.TempDir()
	foreign := t.TempDir()
	for _, dir := range []string{root, foreign} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0700))
	}
	write := func(path, body string) { require.NoError(t, os.WriteFile(path, []byte(body), 0600)) }
	child := "id: child\nsequence: []\n"
	write(filepath.Join(root, "child.yaml"), child)
	write(filepath.Join(foreign, "child.yaml"), child)
	write(filepath.Join(root, "root.yaml"), "id: placeholder\nsequence: []\n")
	expand := func(body string) (InlineResolutionResult, error) {
		rec, err := recipe.LoadRecipeFromString([]byte(body))
		require.NoError(t, err)
		return ResolveInlineRecipes(context.Background(), *rec, InlineResolutionOptions{RootFile: filepath.Join(root, "root.yaml")})
	}
	_, err := expand("id: root\nsequence:\n- include: ./child.yaml\n")
	require.NoError(t, err)
	_, err = expand("id: root\nsequence:\n- include: " + filepath.Join(foreign, "child.yaml") + "\n")
	require.ErrorContains(t, err, "requires perms.limit.jobdb")
	result, err := expand("id: root\nsequence:\n- include: " + filepath.Join(foreign, "child.yaml") + "\n  perms: {limit: {jobdb: {}}}\n")
	require.NoError(t, err)
	node := result.Recipe.RecipeImpl.(*recipe.RecipeSequence).Sequence[0].NodeImpl.(*recipe.NodeSequence)
	require.True(t, node.Internal.Inline.PermissionBoundary)
	require.NotNil(t, node.Perms.Limit.JobDB)
	// A symlink into another checkout is external despite its local spelling.
	require.NoError(t, os.Symlink(filepath.Join(foreign, "child.yaml"), filepath.Join(root, "link.yaml")))
	_, err = expand("id: root\nsequence:\n- include: ./link.yaml\n")
	require.ErrorContains(t, err, "requires perms.limit.jobdb")
	// A foreign nested include must declare its own ceiling, even if its caller did.
	write(filepath.Join(foreign, "child.yaml"), "id: child\nsequence:\n- include: "+filepath.Join(root, "child.yaml")+"\n")
	_, err = expand("id: root\nsequence:\n- include: " + filepath.Join(foreign, "child.yaml") + "\n  perms: {limit: {jobdb: {}}}\n")
	require.ErrorContains(t, err, "requires perms.limit.jobdb")
	// A later permission declaration cannot authorize an earlier foreign include.
	write(filepath.Join(root, "enabled.yaml"), "id: enabled\nperms: {}\nsequence: []\n")
	write(filepath.Join(foreign, "child.yaml"), child)
	_, err = expand("id: root\nsequence:\n- include: " + filepath.Join(foreign, "child.yaml") + "\n- include: ./enabled.yaml\n")
	require.ErrorContains(t, err, "requires perms.limit.jobdb")
}

func TestGitRepositoryIdentityIsConservative(t *testing.T) {
	source := func(url string) inlineRecipeSourceContext { return inlineRecipeSourceContext{GitRepositoryURL: url} }
	require.True(t, sameRecipeRepository(source("https://EXAMPLE.com/team/repo.git"), source("https://example.com/team/repo")))
	for _, url := range []string{"https://example.com/team/other.git", "https://other.example/team/repo.git", "https://example.com/team/repo.git?alias=1", "ssh://example.com/team/repo.git", "https://user@example.com/team/repo.git"} {
		require.False(t, sameRecipeRepository(source("https://example.com/team/repo.git"), source(url)), url)
	}
	require.False(t, sameRecipeRepository(inlineRecipeSourceContext{}, inlineRecipeSourceContext{}))
}

func TestNestedIncludeTrustStaysAnchoredToRootRepository(t *testing.T) {
	root, foreign := t.TempDir(), t.TempDir()
	for _, dir := range []string{root, foreign} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "leaf.yaml"), []byte("id: leaf\nsequence: []\n"), 0600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(foreign, "child.yaml"), []byte("id: child\nsequence:\n- include: ./leaf.yaml\n"), 0600))
	body := fmt.Sprintf("id: root\nsequence:\n- include: ./leaf.yaml\n- include: %q\n  perms: {jobdb: {secrets: {resolve: [foo]}}}\n", filepath.Join(foreign, "child.yaml"))
	rootFile := filepath.Join(root, "root.yaml")
	require.NoError(t, os.WriteFile(rootFile, []byte(body), 0600))
	rec, err := recipe.LoadRecipeFromString([]byte(body))
	require.NoError(t, err)
	expanded, err := ResolveInlineRecipes(context.Background(), *rec, InlineResolutionOptions{RootFile: rootFile})
	require.NoError(t, err)
	nodes := expanded.Recipe.RecipeImpl.(*recipe.RecipeSequence).Sequence
	require.True(t, nodes[0].GetMetadata().Internal.Inline.TrustedSource)
	outer := nodes[1].NodeImpl.(*recipe.NodeSequence)
	require.False(t, outer.Internal.Inline.TrustedSource)
	child := outer.Sequence[0].NodeImpl.(*recipe.NodeSequence)
	nested := child.Sequence[0].GetMetadata().Internal.Inline
	require.False(t, nested.PermissionBoundary, "the two foreign files share a repo")
	require.False(t, nested.TrustedSource, "sharing the caller repo does not make it the root repo")
}

type permissionSourceResolver struct{ *flippingGitResolver }

func (r *permissionSourceResolver) LoadYAML(ctx context.Context, project string, resolution RecipeSourceResolution) ([]byte, error) {
	var socket string
	for _, value := range gitprofile.Env(ctx, nil) {
		if v, ok := strings.CutPrefix(value, gitprofile.SocketEnv+"="); ok {
			socket = v
		}
	}
	if _, err := gitprofile.Call(ctx, socket, gitprofile.Request{Operation: "secret", Name: "ro"}); err != nil {
		return nil, err
	}
	return r.flippingGitResolver.LoadYAML(ctx, project, resolution)
}

func TestIncludeCeilingBoundsNestedSourceReadsWithoutExplicitGitProfile(t *testing.T) {
	for _, useGrant := range []bool{false, true} {
		for _, allowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("grant=%t/allowed=%t", useGrant, allowed), func(t *testing.T) {
				calls := 0
				profile, _ := json.Marshal(gitprofile.Profile{Version: 1, Entries: []gitprofile.Entry{{URL: "https://example.com/", Username: "bot", SecretRef: "ro"}}})
				snapshot := jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{gitprofile.ReadOnlyKey: json.RawMessage(`"read"`), gitprofile.ProfilePrefix + "read": profile}}
				scope, err := gitprofile.NewScope(context.Background(), gitprofile.Selection{}, snapshot, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
					calls++
					return jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{"ro": {Value: "source-read-secret"}}}, nil
				}, "")
				require.NoError(t, err)
				defer scope.Close()
				resolver := &permissionSourceResolver{&flippingGitResolver{commits: []string{strings.Repeat("1", 40)}, files: map[string]string{
					"child.yaml":  "id: child\nsequence:\n- include: ./nested.yaml\n",
					"nested.yaml": "id: nested\nsequence: []\n",
				}}}
				ceiling := "{}"
				if allowed {
					ceiling = "{secrets: {resolve: [ro]}}"
				}
				policy := "{limit: {jobdb: " + ceiling + "}}"
				if useGrant {
					policy = "{jobdb: " + ceiling + "}"
				}
				rec, err := recipe.LoadRecipeFromString([]byte("id: root\nsequence:\n- include: git+https://example.com/foreign.git//child.yaml@main\n  perms: " + policy + "\n"))
				require.NoError(t, err)
				_, err = ResolveInlineRecipes(gitprofile.WithScope(context.Background(), scope), *rec, InlineResolutionOptions{Resolver: resolver})
				if allowed {
					require.NoError(t, err)
					require.Equal(t, 2, calls)
				} else {
					require.Error(t, err)
					require.Equal(t, 1, calls, "caller may bootstrap the external recipe; its nested source read must respect the ceiling")
				}
			})
		}
	}
}

func TestWorkspaceRequiresGuardWithOrWithoutPolicy(t *testing.T) {
	for _, raw := range []string{`{}`, `{"permissions":{"version":1}}`} {
		_, err := resolveWorkspaceTask(context.Background(), jobdb.NewTaskDataOrPanic(json.RawMessage(raw)))
		require.ErrorContains(t, err, "requires executionruntime.WrapTaskWorker")
	}
}
