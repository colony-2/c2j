package jobrestart

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestParsePatchStrictStructure(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `[]`, `true`, `"patch"`, `{`, `{} {}`, `{} false`, `{"inputs":{}}`, `{"config":{}}`,
		`{"project_id":"t","job":{}}`, `{"job":[]}`, `{"job":"bad"}`, `{"scopes":{}}`,
		`{"job":{"workspace":null}}`, `{"job":{"cell_resolution":null}}`,
		`{"job":{"unknown":null}}`, `{"job":{"git":{"not_supported":"lost"}}}`,
		`{"job":{"environment":{"op":{"mystery":null}}}}`,
		`{"job":{"git":{"author":{}}}}`, `{"job":{"artifacts":{"any":{"unknown":null}}}}`,
		`{"scopes":[{"container":"sequence","id":"s","unknown":null}]}`,
		`{"scopes":[{"container":"inputs","id":"s"}]}`, `{"scopes":[{"container":"states","id":"  "}]}`,
		`{"scopes":[{"container":"sequence","id":"s","outputs":[]}]}`,
	} {
		t.Run(raw, func(t *testing.T) { _, err := ParsePatch([]byte(raw)); require.Error(t, err) })
	}
}
func TestParsePatchPreservesExtensibleValues(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"job":{"git":{"author":"new","ref":null}}}`,
		`{"scopes":[{"container":"states","id":"loop","outputs":{"count":5}}]}`,
		`{"job":{"artifacts":{"config":{"kind":"value","value":{"workspace":null,"unknown":[1,true,{"inputs":7}]}}}}}`,
		`{"scopes":[{"container":"sequence","id":"write","outputs":{"workspace":null,"anything":{"inputs":true},"number":9007199254740993,"array":[1,2]}}]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			patch, err := ParsePatch([]byte(raw))
			require.NoError(t, err)
			actual, err := json.Marshal(patch)
			require.NoError(t, err)
			require.JSONEq(t, raw, string(actual))
		})
	}
}
