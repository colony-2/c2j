package compiler

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGroupedInputNamespacesAndPresence(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "original", TaskOrdinal: 2, Name: "start/foo", SizeBytes: 3}
	artifact := &keyedGroupingArtifact{Artifact: jobdb.NewArtifactFromBytes("start/start/foo", []byte("abc")), key: key}
	raw := []byte(`{"start":{"recipe":"batch","inputs":{"creator":true}},"appends":[{"content":{},"artifacts":[]},{"artifacts":[{"kind":"value","name":"inline","format":"text","value":"hello"}]},{"content":{"n":9007199254740993,"nested":[true,null,1.5]},"artifacts":[]}]}`)
	start, files, err := decodeRecipeInput(raw, []jobdb.Artifact{artifact, jobdb.NewArtifactFromBytes("appends/0/batch.recipe.yaml", []byte("bad"))})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "start/foo", files[0].Name())
	got, err := files[0].ArtifactKey()
	require.NoError(t, err)
	require.Equal(t, key, got)
	require.NotNil(t, start.JobContext.Grouping.Appends[0].Content)
	require.Nil(t, start.JobContext.Grouping.Appends[1].Content)
	require.Equal(t, uint64(9007199254740993), uint64(start.JobContext.Grouping.Appends[2].Content["n"].(int64)))
	wire, err := json.Marshal(start.JobContext)
	require.NoError(t, err)
	var restored contextual.JobContext
	require.NoError(t, json.Unmarshal(wire, &restored))
	require.NotNil(t, restored.Grouping.Appends[0].Content)
	require.Equal(t, int64(9007199254740993), restored.Grouping.Appends[2].Content["n"], "durable context must preserve native integer precision")
	ordinary, files, err := decodeRecipeInput([]byte(`{"inputs":{}}`), []jobdb.Artifact{artifact})
	require.NoError(t, err)
	require.Nil(t, ordinary.JobContext.Grouping)
	require.Equal(t, "start/start/foo", files[0].Name())
	for _, bad := range []string{`{"start":{}}`, `{"appends":[]}`, `{"start":null,"appends":[{}]}`, `{"start":{},"appends":[]}`, `{"start":{},"appends":[null]}`, `{"start":{},"appends":[{"content":null,"artifacts":[]}]}`, `{"start":{},"appends":[{"content":{}}]}`} {
		_, _, err := decodeRecipeInput([]byte(bad), nil)
		require.Error(t, err, bad)
	}
}

type keyedGroupingArtifact struct {
	jobdb.Artifact
	key jobdb.ArtifactKey
}

func (a *keyedGroupingArtifact) ArtifactKey() (jobdb.ArtifactKey, error) { return a.key, nil }

func TestGroupingChildRenderingAndNull(t *testing.T) {
	for _, value := range []string{"null", "7", "[]"} {
		var child recipe.ChildGroupChild
		require.NoError(t, yaml.Unmarshal([]byte("recipe: batch\ngrouping: "+value+"\n"), &child))
		require.True(t, child.Grouping.Present)
		_, err := renderChildGroupInput(newChildGroupRenderContext(t, nil, nil), recipe.ChildGroupData{Children: []recipe.ChildGroupChild{child}})
		require.ErrorContains(t, err, "children[0]")
	}
	var child recipe.ChildGroupChild
	require.NoError(t, yaml.Unmarshal([]byte("recipe: batch\ngrouping:\n key: '${{ item.key }}'\n content: {value: '${{ item.value }}'}\n max_items: '${{ inputs.cap }}'\n"), &child))
	ctx := newChildGroupRenderContext(t, map[string]any{"cap": int64(2)}, nil)
	rendered, err := renderChildGroupInput(ctx, recipe.ChildGroupData{ChildrenFrom: []any{map[string]any{"key": "a", "value": true}}, Child: &child})
	require.NoError(t, err)
	var decoded recipeops.ChildGroupStartInput
	require.NoError(t, ops.DecodeWithJsonTags(childGroupInputMap(rendered), &decoded))
	p, err := decoded.Children[0].Grouping.Prepare()
	require.NoError(t, err)
	require.Equal(t, "a", p.Key)
	require.Equal(t, true, p.Contribution.Content["value"])
	require.Equal(t, 2, p.MaxItems)
}
func TestGroupingContextCELAndNestedScope(t *testing.T) {
	job, git := GenerateTestContext()
	job.Grouping = &grouping.Context{Appends: []grouping.Contribution{{Content: map[string]any{"pr": int64(123)}, Artifacts: []artifacts.Ref{artifacts.NewExternalRef("nested/file", "https://example.test/file", false)}}}}
	ctx, err := template.NewRecipeResolutionContext(&git, nil, job)
	require.NoError(t, err)
	for _, scope := range []template.ScopeType{template.ScopeSequence, template.ScopeStateMachine, template.ScopeState, template.ScopeOp} {
		var inputs map[string]any
		if scope == template.ScopeSequence || scope == template.ScopeStateMachine {
			inputs = map[string]any{}
		}
		ctx, err = ctx.NewChildContext(scope, recipe.NodeMetadata{ID: "nested"}, "echo", inputs)
		require.NoError(t, err)
		value, err := ctx.ResolveValueWithMode("${{ context.grouping.appends[0].content.pr }}", template.ModeInterpolation)
		require.NoError(t, err)
		require.EqualValues(t, 123, value)
		value, err = ctx.ResolveValueWithMode("${{ context.grouping.appends[0].artifacts[0].name }}", template.ModeInterpolation)
		require.NoError(t, err)
		require.Equal(t, "nested/file", value)
		value, err = ctx.ResolveValueWithMode("${{ context.metrics.version }}", template.ModeInterpolation)
		require.NoError(t, err)
		require.EqualValues(t, metrics.Version, value, "metrics and grouping must coexist in nested contexts")
		raw, err := json.Marshal(ctx.TaskExecutionContext())
		require.NoError(t, err)
		var restored contextual.TaskExecutionContext
		require.NoError(t, json.Unmarshal(raw, &restored))
		require.Equal(t, ctx.TaskExecutionContext().Grouping, restored.Grouping)
		require.Equal(t, metrics.Version, restored.Metrics.Version)
	}
}
