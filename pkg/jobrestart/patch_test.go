package jobrestart

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/task"
	"github.com/stretchr/testify/require"
)

// JP-01, JP-04, JP-06, JP-08, JP-09, JP-10, JP-14, JP-20.
func TestParsePatchStrictStructure(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `{}`, `{"job":{}}`, `true`, `"patch"`, `[`, `[] {}`, `[] false`,
		`[null]`, `[1]`, `[{}]`, `[{"op":"add","path":"/sequence/s/outputs/x"}]`,
		`[{"op":"invalid","path":"/sequence/s/outputs/x","value":null}]`,
		`[{"op":"copy","path":"/sequence/s/outputs/x"}]`,
		`[{"op":"add","path":null,"value":null}]`,
		`[{"op":"remove","path":""}]`, `[{"op":"remove","path":"context/git"}]`,
		`[{"op":"remove","path":"/sequence/s/outputs/a~2b"}]`,
		`[{"op":"test","path":"/context/workspace","value":null}]`,
		`[{"op":"add","path":"/context/cell_resolution","value":null}]`,
		`[{"op":"remove","path":"/context/git/hash"}]`,
		`[{"op":"remove","path":"/context/git/execution/base_hash"}]`,
		`[{"op":"remove","path":"/context/invocation"}]`,
		`[{"op":"remove","path":"/context/unknown"}]`,
		`[{"op":"remove","path":"/job/git/author"}]`,
		`[{"op":"remove","path":"/inputs/x"}]`,
		`[{"op":"remove","path":"/sequence/s"}]`,
		`[{"op":"remove","path":"/states/s/jobs"}]`,
		`[{"op":"copy","from":"/context/workspace","path":"/states/s/outputs/x"}]`,
		`[{"op":"move","from":"/states/s/outputs/a","path":"/context/git/hash"}]`,
		`[{"op":"move","from":"/states/s/outputs/a","path":"/states/s/outputs/a/b"}]`,
		`[{"op":"add","path":"/context/git","value":{"not_supported":"lost"}}]`,
		`[{"op":"add","path":"/context/git/author","value":{}}]`,
		`[{"op":"add","path":"/context/environment/op","value":{"mystery":null}}]`,
		`[{"op":"add","path":"/context/artifacts/a","value":{"unknown":null}}]`,
	} {
		t.Run(raw, func(t *testing.T) { _, err := ParsePatch([]byte(raw)); require.Error(t, err) })
	}
	_, err := ParsePatch([]byte(`{"job":{}}`))
	require.ErrorContains(t, err, "operation array")
}
func TestParsePatchPreservesExtensibleValues(t *testing.T) {
	for _, raw := range []string{
		`[]`,
		`[{"op":"add","path":"/context/git/author","value":"new"}]`,
		`[{"op":"add","path":"/states/loop/outputs","value":{"count":5}}]`,
		`[{"op":"add","path":"/states/a~1b~0c/outputs/x~1y~0z","value":null}]`,
		`[{"op":"add","path":"/sequence/write/outputs/number","value":9007199254740993}]`,
		`[{"op":"add","path":"/sequence/write/outputs/map","value":{"workspace":null,"anything":{"inputs":true},"array":[1,2]}}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			patch, err := ParsePatch([]byte(raw))
			require.NoError(t, err)
			actual, err := json.Marshal(patch)
			require.NoError(t, err)
			require.Equal(t, raw, string(actual))
		})
	}
	p, err := ParsePatch([]byte(`[{"op":"add","path":"/states/s/outputs/a","value":null,"future":{"ignored":true}}]`))
	require.NoError(t, err)
	require.Equal(t, json.RawMessage(`null`), (*p)[0].Value)
	tokens, err := PointerTokens("/states/a~1b~0c/outputs/~01")
	require.NoError(t, err)
	require.Equal(t, []string{"states", "a/b~c", "outputs", "~1"}, tokens)
}
func TestProgrammaticPatchValidation(t *testing.T) {
	for _, p := range []task.ContextPatch{
		{{Op: "add", Path: "/states/s/outputs/x"}},
		{{Op: "test", Path: "/context/workspace", Value: []byte(`null`)}},
		{{Op: "copy", Path: "/states/s/outputs/x"}},
		{{Op: "replace", Path: "/context/git/author", Value: []byte(`9`)}},
	} {
		require.Error(t, ValidatePatch(&p))
	}
	var p task.ContextPatch
	b, err := json.Marshal(&p)
	require.NoError(t, err)
	require.Equal(t, "[]", string(b))
	require.False(t, strings.Contains(string(b), "null"))
}
