package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type namedRecipeArtifact struct {
	jobdb.Artifact
	name string
}

func (a namedRecipeArtifact) Name() string { return a.name }

func recordedRecipeYAML(t *testing.T, getter jobdb.ArtifactGetter, key jobdb.JobKey, output *jobdb.TaskIO) string {
	t.Helper()
	var artifacts []jobdb.Artifact
	for _, info := range output.Artifacts {
		require.NotNil(t, info.Key)
		artifacts = append(artifacts, namedRecipeArtifact{info.Key.ToLazyArtifact(getter, key.TenantId), info.Name})
	}
	source, err := ParseResolvedRecipeSourceTaskData(&jobdb.SimpleTaskData{Data: output.Data, Artifacts: artifacts})
	require.NoError(t, err)
	require.Empty(t, source.RecipeYAML, "new records must not contain inline YAML")
	raw, err := source.RecipeYAMLBytes(context.Background())
	require.NoError(t, err)
	return string(raw)
}

type recipeArtifactGetter struct {
	t     *testing.T
	key   jobdb.ArtifactKey
	raw   string
	err   error
	calls int
}

func (g *recipeArtifactGetter) GetArtifact(tenant string, key jobdb.ArtifactKey) (jobdb.Artifact, error) {
	g.calls++
	require.Equal(g.t, "tenant", tenant)
	require.Equal(g.t, g.key, key, "use the actual inherited key, not the logical name or current job")
	return jobdb.NewArtifactFromBytes(key.Name, []byte(g.raw)), g.err
}

func TestResolvedRecipeArtifactCompatibility(t *testing.T) {
	const inline = "id: legacy\nsequence: []\n"
	const artifact = "id: snapshot\nsequence: []\n"
	readErr := errors.New("artifact read denied")
	for _, tc := range []struct {
		name, locator, inline, artifact, wantID, wantErr string
		present, readFailure                             bool
	}{
		{name: "inline history", inline: inline, wantID: "legacy"},
		{name: "artifact", locator: "logical.yaml", present: true, artifact: artifact, wantID: "snapshot"},
		{name: "artifact wins", locator: "logical.yaml", present: true, artifact: artifact, inline: inline, wantID: "snapshot"},
		{name: "reserved name", present: true, artifact: artifact, inline: inline, wantID: "snapshot"},
		{name: "absent artifact falls back", locator: "logical.yaml", inline: inline, wantID: "legacy"},
		{name: "missing advertised artifact", locator: "logical.yaml", wantErr: "is missing"},
		{name: "empty artifact", locator: "logical.yaml", present: true, inline: inline, wantErr: "YAML is empty"},
		{name: "malformed artifact", locator: "logical.yaml", present: true, artifact: "sequence: [", inline: inline, wantErr: "parse resolved recipe YAML"},
		{name: "null artifact", locator: "logical.yaml", present: true, artifact: "null\n", inline: inline, wantErr: "parse resolved recipe YAML"},
		{name: "invalid shape", locator: "logical.yaml", present: true, artifact: "garbage: true\n", inline: inline, wantErr: "parse resolved recipe YAML"},
		{name: "failed read", locator: "logical.yaml", present: true, artifact: artifact, inline: inline, readFailure: true, wantErr: "artifact read denied"},
		{name: "legacy metadata only", wantErr: "YAML is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := jobdb.ArtifactKey{JobId: "ancestor-job", TaskOrdinal: 19, Name: "stored-source.yaml", SizeBytes: int64(len(tc.artifact))}
			getter := &recipeArtifactGetter{t: t, key: key, raw: tc.artifact}
			if tc.readFailure {
				getter.err = readErr
			}
			var artifacts []jobdb.Artifact
			if tc.present {
				name := tc.locator
				if name == "" {
					name = ResolvedRecipeYAMLArtifactName
				}
				artifacts = append(artifacts, namedRecipeArtifact{key.ToLazyArtifact(getter, "tenant"), name})
			}
			td := jobdb.NewTaskDataOrPanic(ResolvedRecipeSource{
				RecipeSourceResolution: RecipeSourceResolution{SourceKind: RecipeSourceKindGit, SubmittedSelector: "old-ref"},
				RecipeYAMLArtifact:     tc.locator, RecipeYAML: tc.inline,
			}, artifacts...)
			source, err := ParseResolvedRecipeSourceTaskData(td)
			require.NoError(t, err)
			require.Zero(t, getter.calls, "metadata parsing must not fetch content")
			encoded, err := json.Marshal(source)
			require.NoError(t, err)
			require.JSONEq(t, string(td.GetDataOrPanic()), string(encoded))
			loaded, err := source.LoadRecipeContext(context.Background())
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				if tc.readFailure {
					require.ErrorIs(t, err, readErr)
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantID, loaded.GetMetadata().ID)
			}
			if tc.present {
				require.Equal(t, 1, getter.calls)
			}
		})
	}
}

func TestRootSourceWritesOnlyRecipeArtifact(t *testing.T) {
	ctx := context.Background()
	root := []byte("id: expanded\nsequence:\n - include: child\n   perms: {limit: {jobdb: {}}}\n")
	child := []byte("id: child\nsequence: []\noutputs:\n  text: " + strings.Repeat("x", 1_300_000) + "\n")
	resolution := RecipeSourceResolution{SourceKind: RecipeSourceKindServerRef, SubmittedSelector: "root", ResolvedSelector: "root@pinned", ResolvedCommit: "pinned", WasAlreadyPinned: true}
	resolver := &conventionResolver{
		resolve: func(selector string) (RecipeSourceResolution, error) {
			if selector == "root" {
				return resolution, nil
			}
			return RecipeSourceResolution{SourceKind: RecipeSourceKindServerRef, SubmittedSelector: selector, ResolvedSelector: selector}, nil
		},
		load: func(r RecipeSourceResolution) ([]byte, error) {
			if r.SubmittedSelector == "root" {
				return root, nil
			}
			return child, nil
		},
	}
	rec, err := loadRootRecipe(ctx, resolver, "tenant", resolution)
	require.NoError(t, err)
	expanded, err := ResolveInlineRecipes(ctx, rec, InlineResolutionOptions{ProjectID: "tenant", Resolver: resolver, RootSource: &resolution})
	require.NoError(t, err)
	want, err := marshalRecipeYAML(expanded.Recipe)
	require.NoError(t, err)
	require.Greater(t, len(want), 1_240_000)
	out, err := newRootSourceResolutionTaskWorker(resolver).Run(jobworkflow.TaskContext{}, jobdb.NewTaskDataOrPanic(rootSourceResolutionTaskInput{ProjectID: "tenant", Selector: "root"}))
	require.NoError(t, err)
	raw, err := out.GetData()
	require.NoError(t, err)
	require.Less(t, len(raw), 1024)
	var value map[string]any
	require.NoError(t, json.Unmarshal(raw, &value))
	require.NotContains(t, value, "recipe_yaml")
	require.Equal(t, ResolvedRecipeYAMLArtifactName, value["recipe_yaml_artifact"])
	artifacts, err := out.GetArtifacts()
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	stored, err := artifacts[0].Bytes(ctx)
	require.NoError(t, err)
	require.Equal(t, want, stored)
	source, err := ParseResolvedRecipeSourceTaskData(out)
	require.NoError(t, err)
	require.Equal(t, resolution, source.RecipeSourceResolution)
	loaded, err := source.LoadRecipeContext(ctx)
	require.NoError(t, err)
	roundTrip, err := marshalRecipeYAML(loaded)
	require.NoError(t, err)
	require.Equal(t, string(want), string(roundTrip))
}

type recipeArtifactListError struct {
	jobdb.TaskData
	err error
}

func (d recipeArtifactListError) GetArtifacts() ([]jobdb.Artifact, error) { return nil, d.err }

func TestResolvedRecipeArtifactMetadataErrors(t *testing.T) {
	source := ResolvedRecipeSource{RecipeYAML: "id: legacy\nsequence: []\n"}
	denied := errors.New("artifact enumeration denied")
	_, err := ParseResolvedRecipeSourceTaskData(recipeArtifactListError{jobdb.NewTaskDataOrPanic(source), denied})
	require.ErrorIs(t, err, denied)
	artifact := jobdb.NewArtifactFromBytes(ResolvedRecipeYAMLArtifactName, []byte(source.RecipeYAML))
	_, err = ParseResolvedRecipeSourceTaskData(jobdb.NewTaskDataOrPanic(source, artifact, artifact))
	require.ErrorContains(t, err, "duplicate resolved recipe artifact")
	metadata, err := ParseResolvedRecipeSourceJSON([]byte(`{"recipe_yaml_artifact":"source.yaml","recipe_yaml":"id: stale\nsequence: []\n"}`))
	require.NoError(t, err)
	_, err = metadata.LoadRecipe()
	require.ErrorContains(t, err, "requires task data")
	_, err = ParseResolvedRecipeSourceJSON([]byte(`{"source_kind":"git","recipe_yaml_artifact":123}`))
	require.ErrorContains(t, err, "decode resolved recipe source payload")
}
