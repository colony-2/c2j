package compiler

import (
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	recipetemplate "github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/template/colonycel"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestResolveArtifactBindingsExpandsContextArtifactsAtRoot(t *testing.T) {
	brief := artifactRefForTest("brief.md")
	requirements := artifactRefForTest("docs/requirements.md")
	resCtx := artifactBindingResolutionContext(t, map[string]recipeartifacts.Ref{
		"brief.md":             brief,
		"docs/requirements.md": requirements,
	})

	got, err := resolveArtifactBindings(resCtx, map[string]interface{}{
		"./": `${{ context.artifacts }}`,
	})

	require.NoError(t, err)
	require.Equal(t, map[string]recipeartifacts.Ref{
		"brief.md":             brief,
		"docs/requirements.md": requirements,
	}, got)
}

func TestResolveArtifactBindingsExpandsSequenceAndStateArtifactsUnderPrefixes(t *testing.T) {
	prepare := artifactRefForTest("prepare/report.md")
	plan := artifactRefForTest("plan.json")
	resCtx := artifactBindingResolutionContext(t, nil)
	resCtx.TemplateData.Sequence["prepare"] = recipetemplate.StepOutput{
		Outputs: map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{
			"prepare/report.md": prepare,
		},
	}
	resCtx.TemplateData.States["plan"] = recipetemplate.StepOutput{
		Outputs: map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{
			"plan.json": plan,
		},
	}

	got, err := resolveArtifactBindings(resCtx, map[string]interface{}{
		"previous": `${{ sequence.prepare.artifacts }}`,
		"state/":   `${{ states.plan.artifacts }}`,
	})

	require.NoError(t, err)
	require.Equal(t, map[string]recipeartifacts.Ref{
		"previous/prepare/report.md": prepare,
		"state/plan.json":            plan,
	}, got)
}

func TestResolveArtifactBindingsCombinesExplicitAndSetBindings(t *testing.T) {
	brief := artifactRefForTest("brief.md")
	requirements := artifactRefForTest("requirements.md")
	resCtx := artifactBindingResolutionContext(t, map[string]recipeartifacts.Ref{
		"brief.md":        brief,
		"requirements.md": requirements,
	})

	got, err := resolveArtifactBindings(resCtx, map[string]interface{}{
		"submitted/":         `${{ context.artifacts }}`,
		"canonical-brief.md": `${{ context.artifacts["brief.md"] }}`,
	})

	require.NoError(t, err)
	require.Equal(t, map[string]recipeartifacts.Ref{
		"submitted/brief.md":        brief,
		"submitted/requirements.md": requirements,
		"canonical-brief.md":        brief,
	}, got)
}

func TestResolveArtifactBindingsExpandsArtifactNamesThatLookLikeRefFields(t *testing.T) {
	kind := artifactRefForTest("kind")
	stored := artifactRefForTest("stored")
	resCtx := artifactBindingResolutionContext(t, map[string]recipeartifacts.Ref{
		"kind":   kind,
		"stored": stored,
	})

	got, err := resolveArtifactBindings(resCtx, map[string]interface{}{
		"submitted/": `${{ context.artifacts }}`,
	})

	require.NoError(t, err)
	require.Equal(t, map[string]recipeartifacts.Ref{
		"submitted/kind":   kind,
		"submitted/stored": stored,
	}, got)
}

func TestResolveArtifactBindingsExpandsFilteredArtifactLists(t *testing.T) {
	archive := artifactRefForTest("build/app.zip")
	log := artifactRefForTest("build/app.log")
	opts := recipetemplate.DefaultResolutionOptions()
	opts.CELOptionsProvider = colonycel.NewBuilder(colonycel.Options{})
	resCtx := artifactBindingResolutionContextWithOptions(t, nil, opts)
	resCtx.TemplateData.Sequence["build"] = recipetemplate.StepOutput{
		Outputs: map[string]interface{}{},
		Artifacts: map[string]recipeartifacts.Ref{
			"build/app.zip": archive,
			"build/app.log": log,
		},
	}

	got, err := resolveArtifactBindings(resCtx, map[string]interface{}{
		"release/": `${{ artifact_filter(sequence.build.artifacts, {"name_suffix": ".zip"}) }}`,
	})

	require.NoError(t, err)
	require.Equal(t, map[string]recipeartifacts.Ref{
		"release/build/app.zip": archive,
	}, got)
}

func TestResolveArtifactBindingsRejectsDuplicateExpandedDestinations(t *testing.T) {
	brief := artifactRefForTest("brief.md")
	resCtx := artifactBindingResolutionContext(t, map[string]recipeartifacts.Ref{
		"brief.md": brief,
	})

	_, err := resolveArtifactBindings(resCtx, map[string]interface{}{
		"./":       `${{ context.artifacts }}`,
		"brief.md": `${{ context.artifacts["brief.md"] }}`,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), `artifact binding destination "brief.md"`)
}

func TestResolveArtifactBindingsRejectsFileDirectoryConflicts(t *testing.T) {
	brief := artifactRefForTest("brief.md")
	docs := artifactRefForTest("docs")
	resCtx := artifactBindingResolutionContext(t, map[string]recipeartifacts.Ref{
		"brief.md": brief,
		"docs":     docs,
	})

	_, err := resolveArtifactBindings(resCtx, map[string]interface{}{
		"docs":  `${{ context.artifacts["docs"] }}`,
		"docs/": `${{ context.artifacts }}`,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), `conflicts`)
}

func artifactBindingResolutionContext(t *testing.T, artifactRefs map[string]recipeartifacts.Ref) *recipetemplate.ResolutionContext {
	t.Helper()
	return artifactBindingResolutionContextWithOptions(t, artifactRefs, recipetemplate.DefaultResolutionOptions())
}

func artifactBindingResolutionContextWithOptions(t *testing.T, artifactRefs map[string]recipeartifacts.Ref, opts recipetemplate.ResolutionOptions) *recipetemplate.ResolutionContext {
	t.Helper()
	resCtx, err := recipetemplate.NewRecipeResolutionContext(
		&contextual.GitCommitContext{},
		map[string]interface{}{},
		contextual.JobContext{Artifacts: artifactRefs},
		opts,
	)
	require.NoError(t, err)
	return resCtx
}

func artifactRefForTest(name string) recipeartifacts.Ref {
	return recipeartifacts.NewStoredRef(jobdb.ArtifactKey{
		JobId:       "job",
		TaskOrdinal: 1,
		Name:        name,
		SizeBytes:   int64(len(name)),
	})
}
