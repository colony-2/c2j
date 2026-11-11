package template

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func patchFixture(t *testing.T) *ResolutionContext {
	t.Helper()
	root, err := NewRecipeResolutionContext(&contextual.GitCommitContext{PersistHash: "keep-commit"}, nil, contextual.JobContext{Workflow: contextual.WorkflowContext{GoalID: "before"}, GitBase: contextual.GitBaseContext{BaseRef: "main"}})
	require.NoError(t, err)
	seq, err := root.NewChildContext(ScopeSequence, recipe.NodeMetadata{ID: "s"}, "", map[string]any{})
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, decodePatchDocument([]byte(`{"c":[{},{},{},{},{"b":{"c":[0,1,2],"keep":true}}],"array":["a","b","c"],"object":{"key":"before"},"n":9007199254740993,"decimal":1.23000000000000000001,"null":null,"a/b~c":"escaped"}`), &out))
	seq.TemplateData.Sequence["a/b~c"] = StepOutput{Outputs: out}
	return seq
}
func requirePatchJSON(t *testing.T, expected string, actual any) {
	t.Helper()
	var a, b any
	require.NoError(t, decodePatchDocument([]byte(expected), &a))
	raw, err := json.Marshal(actual)
	require.NoError(t, err)
	require.NoError(t, decodePatchDocument(raw, &b))
	require.True(t, patchEqual(a, b), "expected %s, got %s", expected, raw)
}

// JP-02 through JP-07: ordering, insertion/removal/move/copy, exact numbers,
// null, escaped IDs/keys and dependent edits use the real application adapter.
func TestJSONPatchRFCOperations(t *testing.T) {
	const base = "/sequence/a~1b~0c/outputs"
	cases := []struct{ name, patch, expected string }{
		{"nested", `[{"op":"replace","path":"` + base + `/c/4/b/c/2","value":"foo"}]`, `[0,1,"foo"]`},
		{"append", `[{"op":"add","path":"` + base + `/array/-","value":"d"}]`, `["a","b","c","d"]`},
		{"insert at length", `[{"op":"add","path":"` + base + `/array/3","value":"d"}]`, `["a","b","c","d"]`},
		{"insert", `[{"op":"add","path":"` + base + `/array/1","value":"x"}]`, `["a","x","b","c"]`},
		{"move", `[{"op":"move","from":"` + base + `/array/0","path":"` + base + `/array/2"}]`, `["b","c","a"]`},
		{"remove", `[{"op":"remove","path":"` + base + `/array/1"}]`, `["a","c"]`},
		{"ordered", `[{"op":"add","path":"` + base + `/array/1","value":null},{"op":"test","path":"` + base + `/array/1","value":null},{"op":"replace","path":"` + base + `/array/2","value":"new"}]`, `["a",null,"new","c"]`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rc := patchFixture(t)
			require.NoError(t, rc.ApplyContextPatch(parseTestPatch(t, tt.patch)))
			out := rc.TemplateData.Sequence["a/b~c"].Outputs
			if tt.name == "nested" {
				requirePatchJSON(t, tt.expected, out["c"].([]any)[4].(map[string]any)["b"].(map[string]any)["c"])
			} else {
				requirePatchJSON(t, tt.expected, out["array"])
			}
			raw, err := json.Marshal(out)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"n":9007199254740993`)
			require.Contains(t, string(raw), `"decimal":1.23000000000000000001`)
		})
	}
	t.Run("copy isolation and escaped keys", func(t *testing.T) {
		rc := patchFixture(t)
		require.NoError(t, rc.ApplyContextPatch(parseTestPatch(t, `[
   {"op":"copy","from":"`+base+`/object","path":"`+base+`/copied"},
   {"op":"replace","path":"`+base+`/object/key","value":"after"},
   {"op":"replace","path":"`+base+`/a~1b~0c","value":null},
   {"op":"remove","path":"`+base+`/null"}
  ]`)))
		out := rc.TemplateData.Sequence["a/b~c"].Outputs
		require.Equal(t, "before", out["copied"].(map[string]any)["key"])
		require.Contains(t, out, "a/b~c")
		require.Nil(t, out["a/b~c"])
		require.NotContains(t, out, "null")
	})
	t.Run("numeric equality", func(t *testing.T) {
		rc := patchFixture(t)
		require.NoError(t, rc.ApplyContextPatch(parseTestPatch(t, `[
   {"op":"test","path":"`+base+`/n","value":9007199254740993.0},
   {"op":"test","path":"`+base+`/c/4/b/c/1","value":1e0},
   {"op":"test","path":"`+base+`/object","value":{"key":"before"}}
  ]`)))
		require.ErrorContains(t, rc.ApplyContextPatch(parseTestPatch(t, `[{"op":"test","path":"`+base+`/n","value":9007199254740992}]`)), "test failed")
	})
}

// JP-04, JP-05, JP-09 through JP-11: failure after an earlier successful write
// cannot publish output/context/ancestor/workspace changes.
func TestJSONPatchFailureAtomicity(t *testing.T) {
	const base = "/sequence/a~1b~0c/outputs"
	for _, tail := range []string{
		`{"op":"replace","path":"` + base + `/missing","value":1}`,
		`{"op":"remove","path":"` + base + `/missing"}`,
		`{"op":"test","path":"` + base + `/missing","value":null}`,
		`{"op":"test","path":"` + base + `/n","value":9007199254740992}`,
		`{"op":"add","path":"` + base + `/missing/child","value":1}`,
		`{"op":"add","path":"` + base + `/array/-1","value":1}`,
		`{"op":"replace","path":"` + base + `/array/01","value":1}`,
		`{"op":"replace","path":"` + base + `/array/3","value":1}`,
		`{"op":"remove","path":"` + base + `/array/-"}`,
		`{"op":"copy","from":"` + base + `/array/01","path":"` + base + `/x"}`,
		`{"op":"add","path":"/sequence/hidden/outputs/x","value":1}`,
		`{"op":"replace","path":"` + base + `","value":null}`,
		`{"op":"replace","path":"` + base + `","value":[]}`,
		`{"op":"copy","from":"` + base + `/object","path":"/context/git"}`,
		`{"op":"copy","from":"` + base + `/object","path":"/context/git"},{"op":"remove","path":"/context/git"}`,
		`{"op":"copy","from":"` + base + `/n","path":"/context/workflow/goal_id"}`,
	} {
		t.Run(tail, func(t *testing.T) {
			rc := patchFixture(t)
			before, err := json.Marshal(rc.TemplateData)
			require.NoError(t, err)
			parent, err := json.Marshal(rc.Parent.TemplateData)
			require.NoError(t, err)
			commit := *rc.commitContext
			var workspace *contextual.GitBaseContext
			if rc.workspaceBase != nil {
				v := *rc.workspaceBase
				workspace = &v
			}
			raw := `[{"op":"replace","path":"/context/workflow/goal_id","value":"changed"},{"op":"replace","path":"` + base + `/object/key","value":"changed"},` + tail + `]`
			require.Error(t, rc.ApplyContextPatch(parseTestPatch(t, raw)))
			after, err := json.Marshal(rc.TemplateData)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
			after, err = json.Marshal(rc.Parent.TemplateData)
			require.NoError(t, err)
			require.Equal(t, string(parent), string(after))
			require.Equal(t, commit, *rc.commitContext)
			require.Equal(t, workspace, rc.workspaceBase)
		})
	}
}

// JP-10, JP-12, JP-13: typed map edits, propagation of calculated values,
// preservation of divergent ancestor fields, CEL roots, and Git boundaries.
func TestJSONPatchPropagationAndCEL(t *testing.T) {
	rc := patchFixture(t)
	rc.Parent.TemplateData.Context.Workflow.CellName = "ancestor-only"
	rc.TemplateData.Context.Workflow.CellName = "selected"
	require.NoError(t, rc.ApplyContextPatch(parseTestPatch(t, `[
  {"op":"replace","path":"/context/workflow/goal_id","value":"patched"},
  {"op":"add","path":"/sequence/a~1b~0c/outputs/array/-","value":4},
  {"op":"add","path":"/context/artifacts","value":{}},
  {"op":"add","path":"/context/artifacts/any","value":{"kind":"value","value":{"unknown":[null,1]}}}
 ]`)))
	require.Equal(t, "patched", rc.Parent.TemplateData.Context.Workflow.GoalID)
	require.Equal(t, "ancestor-only", rc.Parent.TemplateData.Context.Workflow.CellName)
	require.Contains(t, rc.TemplateData.Context.Artifacts, "any")
	value, err := rc.ResolveValueWithLocals("${{ context.workflow.goal_id }}", nil)
	require.NoError(t, err)
	require.Equal(t, "patched", value)
	value, err = rc.ResolveValueWithLocals("${{ sequence['a/b~c'].outputs.array[3] + 1 }}", nil)
	require.NoError(t, err)
	require.EqualValues(t, 5, value)
}

// JP-17, JP-18: legacy is accepted only at the persisted reader; malformed
// stored data never falls through to another format.
func TestRestartPayloadShapeBoundary(t *testing.T) {
	rc := patchFixture(t)
	require.NoError(t, rc.ApplyRestartPayload([]byte(`{"job":{"workflow":{"goal_id":"legacy"}},"scopes":[{"container":"sequence","id":"a/b~c","outputs":{"null":null,"array":[9]}}]}`)))
	require.Equal(t, "legacy", rc.TemplateData.Context.Workflow.GoalID)
	require.NotContains(t, rc.TemplateData.Sequence["a/b~c"].Outputs, "null")
	requirePatchJSON(t, `[9]`, rc.TemplateData.Sequence["a/b~c"].Outputs["array"])
	for _, raw := range []string{`null`, `{} {}`, `{"unknown":1}`, `[{"op":"bad"}]`, `[] false`} {
		require.Error(t, rc.ApplyRestartPayload([]byte(raw)))
	}
}

// JP-11, JP-12: calculated arrays propagate once despite different ancestor
// baselines, and failed edits cannot alter shared workspace state.
func TestJSONPatchArrayPropagationAcrossWorkspace(t *testing.T) {
	rc := patchFixture(t)
	root := rc.Parent
	root.TemplateData.Context.Artifacts = map[string]artifacts.Ref{"data": {Kind: artifacts.RefKindValue, Value: []byte(`{"array":["ancestor"],"unrelated":"ancestor"}`)}}
	rc.TemplateData.Context.Artifacts = map[string]artifacts.Ref{"data": {Kind: artifacts.RefKindValue, Value: []byte(`{"array":["selected"],"unrelated":"selected","selected_only":true}`)}}
	scoped, err := rc.WithWorkspace(contextual.WorkspaceContext{Cell: "other", ScopeID: "other"}, contextual.GitBaseContext{BaseRepo: "other", BaseRef: "main"})
	require.NoError(t, err)
	scoped.TemplateData.Context.GitTask.Execution = contextual.GitExecutionReferences{BaseHash: "actual-base", HeadHash: "actual-head"}
	originalBase := *scoped.workspaceBase
	originalContext, err := json.Marshal(scoped.TemplateData)
	require.NoError(t, err)
	fail := parseTestPatch(t, `[{"op":"add","path":"/context/git/author","value":"new"},{"op":"test","path":"/context/git/ref","value":"wrong"}]`)
	require.Error(t, scoped.ApplyContextPatch(fail))
	after, err := json.Marshal(scoped.TemplateData)
	require.NoError(t, err)
	require.Equal(t, originalContext, after)
	require.Equal(t, originalBase, *scoped.workspaceBase)
	p := parseTestPatch(t, `[{"op":"add","path":"/context/artifacts/data/value/array/-","value":"appended"},{"op":"add","path":"/context/git/author","value":"new"}]`)
	require.NoError(t, scoped.ApplyContextPatch(p))
	requirePatchJSON(t, `{"array":["selected","appended"],"unrelated":"ancestor"}`, root.TemplateData.Context.Artifacts["data"].Value)
	requirePatchJSON(t, `{"array":["selected","appended"],"unrelated":"selected","selected_only":true}`, rc.TemplateData.Context.Artifacts["data"].Value)
	require.Equal(t, "new", scoped.workspaceBase.GitAuthor)
	require.Empty(t, root.TemplateData.Context.GitTask.GitAuthor)
	require.Equal(t, contextual.GitExecutionReferences{BaseHash: "actual-base", HeadHash: "actual-head"}, scoped.TemplateData.Context.GitTask.Execution)
}

func TestJSONPatchOutputAndTestOnlyPreserveWorkspaceCaches(t *testing.T) {
	rc := patchFixture(t)
	scoped, err := rc.WithWorkspace(contextual.WorkspaceContext{Cell: "other", ScopeID: "other", RefPolicy: contextual.WorkspaceFollowPerTask}, contextual.GitBaseContext{BaseRepo: "other", BaseRef: "main", ResolvedBaseHash: "original-base"})
	require.NoError(t, err)
	scoped.commitContext.ParentHash = "next-commit"
	before := scoped.TemplateData.Context
	base := *scoped.workspaceBase
	require.NoError(t, scoped.ApplyContextPatch(parseTestPatch(t, `[{"op":"add","path":"/sequence/a~1b~0c/outputs/array/-","value":"next"}]`)))
	require.Equal(t, before, scoped.TemplateData.Context)
	require.Equal(t, base, *scoped.workspaceBase)
	require.NoError(t, scoped.ApplyContextPatch(parseTestPatch(t, `[{"op":"test","path":"/sequence/a~1b~0c/outputs/array/3","value":"next"}]`)))
	require.Equal(t, before, scoped.TemplateData.Context)
	require.Equal(t, base, *scoped.workspaceBase)
	require.NoError(t, scoped.ApplyContextPatch(parseTestPatch(t, `[{"op":"replace","path":"/context/workflow/goal_id","value":"changed"}]`)))
	require.Equal(t, before.GitTask, scoped.TemplateData.Context.GitTask)
	require.Equal(t, base, *scoped.workspaceBase)
}

func TestRestartPayloadExactInlineArtifactAndLegacyValues(t *testing.T) {
	const values = `{"n":9007199254740993,"nested":[9007199254740995,18446744073709551617,1.23000000000000000001]}`
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "json-patch", true: "historical-merge"}[legacy], func(t *testing.T) {
			rc := patchFixture(t)
			ref := artifacts.Ref{Kind: artifacts.RefKindValue, Format: "json", Value: json.RawMessage(values)}
			rc.TemplateData.Context.Artifacts = map[string]artifacts.Ref{"exact": ref}
			var raw []byte
			if legacy {
				raw = []byte(`{"job":{"git":{"author":"changed"}},"scopes":[{"container":"sequence","id":"a/b~c","outputs":{"exact":` + values + `}}]}`)
			} else {
				raw = []byte(`[{"op":"add","path":"/context/git/author","value":"changed"},{"op":"add","path":"/sequence/a~1b~0c/outputs/exact","value":` + values + `}]`)
			}
			require.NoError(t, rc.ApplyRestartPayload(raw))
			require.Equal(t, "changed", rc.TemplateData.Context.GitTask.GitAuthor)
			requirePatchJSON(t, values, rc.TemplateData.Sequence["a/b~c"].Outputs["exact"])
			requirePatchJSON(t, values, json.RawMessage(rc.TemplateData.Context.Artifacts["exact"].Value))
			require.Equal(t, artifacts.RefKindValue, rc.TemplateData.Context.Artifacts["exact"].Kind)
		})
	}
}
