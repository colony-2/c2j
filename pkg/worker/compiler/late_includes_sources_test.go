package compiler

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git/selectorcache"
	"github.com/colony-2/c2j/pkg/recipe"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeRecursiveSourcesAndOfflineCEL(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newLateGitFixture(t)
			commitA := commitHookFile(t, f.dir, "flows/old.yaml", "id: old\nsequence: []\noutputs: {value: A}\n")
			commitHookFile(t, f.dir, "flows/steps/grand/leaf.yaml", "id: grand\nsequence: []\noutputs: {value: wrong-A}\n")
			commitHookFile(t, f.dir, "flows/steps/child.yaml", "id: child\nsequence:\n- id: grand\n  include: ./grand/leaf.yaml\noutputs: {value: '{{ sequence.grand.outputs.value }}'}\n")
			writeFunctionPackage(t, filepath.Join(f.dir, "tools", "functions"), `name: source_functions
functions:
- name: mark_source
  mode: function
  execution: ./marker-link.sh
  args: [{name: value, schema: {type: string}}]
  returns: {schema: {type: string}}
`, map[string]string{"marker.sh": "#!/bin/sh\ncat; printf -- '-B'"})
			require.NoError(t, os.Chmod(filepath.Join(f.dir, "tools/functions/marker.sh"), 0755))
			require.NoError(t, os.Symlink("marker.sh", filepath.Join(f.dir, "tools/functions/marker-link.sh")))
			commitHookFile(t, f.dir, "flows/steps/grand/leaf.yaml", "id: grand-at-B\nversion: 'B'\nsequence: []\noutputs: {value: B}\n")
			commitB := commitHookFile(t, f.dir, "flows/main.yaml", fmt.Sprintf(`id: selected-B
input_schema:
 nested_commit: {type: string, required: true}
inputs: {nested_commit: '{{ inputs.nested_commit }}'}
extensions:
 functions: [{selector: ./tools/functions, include: [mark_source]}]
sequence:
- id: relative
  include: ./steps/child.yaml
- id: explicit
  include: %s
- id: nested
  include: {bind: late, recipe: %q}
outputs:
 relative: '${{ mark_source(sequence.relative.outputs.value) }}'
 explicit: '${{ sequence.explicit.outputs.value }}'
 nested: '${{ sequence.nested.outputs.value }}'
`, f.selector("flows/old.yaml", commitA), f.selector("flows/steps/child.yaml", "{{ inputs.nested_commit }}")))
			// main's nested selector receives the selected B commit in caller context.
			rec := f.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n  inputs: {nested_commit: %q}\noutputs: {values: '${{ sequence.call.outputs }}'}\n", f.selector("flows/main.yaml", commitB), commitB))
			rt := newTimeoutReproRuntime(t, backend)
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			out := lateRun(t, guard, key, w, lateTasks(t, guard)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Equal(t, map[string]any{"relative": "B-B", "explicit": "A", "nested": "B"}, lateJobOutput(t, rt, key)["values"])
			s, c, run := lateRecordOutputs(t, rt, key)
			require.Len(t, s, 2)
			require.Len(t, c, 2)
			require.Equal(t, commitB, s[0].Commit)
			require.Equal(t, commitB, s[1].Commit)
			require.Equal(t, commitB, s[1].Declaration.DeclaringSource.ResolvedCommit)
			bytes := lateArtifactBytes(t, rt, key, run)
			require.Len(t, bytes, 2)
			captured, err := recipe.LoadInternalRecipeFromString(bytes[0])
			require.NoError(t, err)
			require.NotEmpty(t, captured.GetMetadata().Internal.FunctionPackages)
			require.NotEmpty(t, c[0].Functions.ResolvedSelectors)
			var sources []recipe.RecipeSourceSnapshot
			var walk func(recipe.Node)
			walk = func(n recipe.Node) {
				if m := n.GetMetadata().Internal; m != nil && m.Inline != nil {
					sources = append(sources, m.Inline.Source)
				}
				if n, ok := n.NodeImpl.(*recipe.NodeSequence); ok {
					for _, child := range n.Sequence {
						walk(child)
					}
				}
			}
			root, _, _, err := includedRecipeRootNode(*captured)
			require.NoError(t, err)
			walk(root)
			require.Len(t, sources, 3)
			require.Equal(t, commitB, sources[0].ResolvedCommit)
			require.Contains(t, sources[0].ResolvedSelector, "flows/steps/child.yaml@")
			require.Equal(t, commitB, sources[1].ResolvedCommit)
			require.Contains(t, sources[1].ResolvedSelector, "flows/steps/grand/leaf.yaml@")
			require.Equal(t, commitA, sources[2].ResolvedCommit)
			// Remove all recipe Git access and the ordinary extension cache. Rebuilding
			// the provider and evaluating outputs must use the captured function files.
			f.available.Store(false)
			require.NoError(t, os.RemoveAll(f.dir))
			cache := selectorcache.Default()
			oldRoot := cache.Root
			cache.Root = t.TempDir()
			defer func() { cache.Root = oldRoot }()
			reads := f.reads.Load()
			require.NoError(t, probeReplay(t, rt, key, w))
			require.Equal(t, reads, f.reads.Load())
		})
	}
}

func TestLateIncludeForeignSourceChainAndCEL(t *testing.T) {
	root := newLateGitFixture(t)
	foreign := newLateGitFixture(t)
	leaf := newLateGitFixture(t)
	for _, item := range []struct {
		f           *lateGitFixture
		name, value string
	}{{foreign, "foreign_mark", "F"}, {leaf, "leaf_mark", "Z"}} {
		writeFunctionPackage(t, filepath.Join(item.f.dir, "tools", "functions"), fmt.Sprintf("name: %s\nfunctions:\n- name: %s\n  mode: function\n  execution: sh marker.sh\n  args: [{name: value, schema: {type: string}}]\n  returns: {schema: {type: string}}\n", item.name, item.name), map[string]string{"marker.sh": fmt.Sprintf("printf '%s'", item.value)})
	}
	leafCommit := commitHookFile(t, leaf.dir, "flows/leaf.yaml", "id: foreign-leaf\nextensions:\n functions: [{selector: ./tools/functions, include: [leaf_mark]}]\nsequence: []\noutputs: {value: '${{ leaf_mark(\"\") }}'}\n")
	b := commitHookFile(t, foreign.dir, "flows/B/main.yaml", fmt.Sprintf("id: foreign-B\nextensions:\n functions: [{selector: ./tools/functions, include: [foreign_mark]}]\nsequence:\n- id: leaf\n  include: %q\n  perms: {jobdb: {}}\noutputs: {value: '${{ foreign_mark(\"\") + sequence.leaf.outputs.value }}'}\n", leaf.selector("flows/leaf.yaml", leafCommit)))
	a := commitHookFile(t, foreign.dir, "flows/A/main.yaml", fmt.Sprintf("id: foreign-A\nsequence:\n- id: newer\n  include: {bind: late, recipe: %q}\noutputs: {value: '${{ sequence.newer.outputs.value }}'}\n", foreign.selector("flows/B/main.yaml", b)))
	rec := root.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: foreign\n  include: {bind: late, recipe: %q}\n  perms: {jobdb: {}}\noutputs: {value: '${{ sequence.foreign.outputs.value }}'}\n", foreign.selector("flows/A/main.yaml", a)))
	rt := newTimeoutReproRuntime(t, "sqlite")
	guard := lateGuard(rt)
	w := lateWorker(t, rec, nil)
	key := probeSubmit(t, rt, w)
	out := lateRun(t, guard, key, w, lateTasks(t, guard)...)
	require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
	require.Equal(t, "FZ", lateJobOutput(t, rt, key)["value"])
	s, c, run := lateRecordOutputs(t, rt, key)
	require.Len(t, s, 2)
	require.Len(t, c, 2)
	require.True(t, s[0].PermissionBoundary)
	require.False(t, s[0].TrustedSource)
	require.False(t, s[1].PermissionBoundary)
	require.False(t, s[1].TrustedSource)
	require.Equal(t, s[0].Repository, s[1].Repository)
	require.Equal(t, a, s[1].Declaration.DeclaringSource.ResolvedCommit)
	require.Equal(t, s[0].Declaration.RootRepository, s[1].Declaration.RootRepository)
	captured := lateArtifactBytes(t, rt, key, run)
	require.Len(t, captured, 2)
	parsed, err := recipe.LoadInternalRecipeFromString(captured[1])
	require.NoError(t, err)
	z := parsed.RecipeImpl.(*recipe.RecipeSequence).Sequence[0].GetMetadata().Internal.Inline
	require.True(t, z.PermissionBoundary)
	require.False(t, z.TrustedSource)
	require.Equal(t, leafCommit, z.Source.ResolvedCommit)
	packages := parsed.GetMetadata().Internal.FunctionPackages
	require.Len(t, packages, 2)
	commits := []string{packages[0].ResolvedCommit, packages[1].ResolvedCommit}
	require.Contains(t, commits, b)
	require.Contains(t, commits, leafCommit)
	for _, f := range []*lateGitFixture{root, foreign, leaf} {
		f.available.Store(false)
		require.NoError(t, os.RemoveAll(f.dir))
	}
	cache := selectorcache.Default()
	old := cache.Root
	cache.Root = t.TempDir()
	defer func() { cache.Root = old }()
	require.NoError(t, probeReplay(t, rt, key, w))
}
