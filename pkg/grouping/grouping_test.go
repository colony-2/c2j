package grouping

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestGroupingValidation(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"null", "null", false}, {"scalar", "1", false}, {"array", "[]", false},
		{"missing key", `{"content":{}}`, false}, {"numeric key", `{"key":1,"content":{}}`, false},
		{"empty key", `{"key":"","content":{}}`, false}, {"blank key", `{"key":" \t","content":{}}`, false},
		{"NUL key", `{"key":"x\u0000","content":{}}`, false},
		{"empty content", `{"key":"k","content":{}}`, true}, {"content and empty artifacts", `{"key":"k","content":{},"artifacts":[]}`, true},
		{"null content", `{"key":"k","content":null}`, false}, {"scalar content", `{"key":"k","content":3}`, false},
		{"array content", `{"key":"k","content":[]}`, false}, {"missing work", `{"key":"k"}`, false},
		{"empty artifacts", `{"key":"k","artifacts":[]}`, false}, {"null artifacts", `{"key":"k","content":{},"artifacts":null}`, false},
		{"object artifacts", `{"key":"k","content":{},"artifacts":{}}`, false}, {"unknown field", `{"key":"k","content":{},"unknown":1}`, false},
		{"value only", `{"key":"k","artifacts":[{"kind":"value","name":"a","format":"text","value":"hello"}]}`, true},
		{"null plus files", `{"key":"k","content":null,"artifacts":[{"kind":"value","name":"a","format":"text","value":"hello"}]}`, false},
		{"scalar plus files", `{"key":"k","content":3,"artifacts":[{"kind":"value","name":"a","format":"text","value":"hello"}]}`, false},
		{"array plus files", `{"key":"k","content":[],"artifacts":[{"kind":"value","name":"a","format":"text","value":"hello"}]}`, false},
		{"cap without work", `{"key":"k","max_items":50}`, false},
		{"cap and empty files", `{"key":"k","max_items":50,"artifacts":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s Spec
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &s))
			_, err := s.Prepare()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	for _, key := range []string{"k", " k ", strings.Repeat("x", 1024), strings.Repeat("λ", 512)} {
		p, err := (Spec{true, map[string]any{"key": key, "content": map[string]any{}}}).Prepare()
		require.NoError(t, err)
		require.Equal(t, key, p.Key)
	}
	for _, key := range []string{strings.Repeat("x", 1025), string([]byte{0xff})} {
		_, err := (Spec{true, map[string]any{"key": key, "content": map[string]any{}}}).Prepare()
		require.Error(t, err)
	}
	for _, tc := range []struct {
		value any
		want  int
		valid bool
	}{
		{nil, 0, false}, {false, 0, false}, {"1", 0, false}, {1.5, 0, false}, {-1, 0, false}, {1001, 0, false},
		{0, 50, true}, {1, 1, true}, {50, 50, true}, {1000, 1000, true},
	} {
		p, err := (Spec{true, map[string]any{"key": "k", "content": map[string]any{}, "max_items": tc.value}}).Prepare()
		if tc.valid {
			require.NoError(t, err)
			require.Equal(t, tc.want, p.MaxItems)
		} else {
			require.Error(t, err)
		}
	}
}

func TestContributionReferences(t *testing.T) {
	key := jobdb.ArtifactKey{JobId: "source", TaskOrdinal: 1, Name: "dir/file", SizeBytes: 5}
	first := artifacts.NewStoredRef(key)
	first.Name = "first"
	second := first
	second.Name = "second"
	external := artifacts.NewExternalRef("external", "https://example.test/file", false)
	p, err := (Spec{true, map[string]any{"key": "k", "artifacts": []artifacts.Ref{first, second, external}}}).Prepare()
	require.NoError(t, err)
	require.Equal(t, []jobdb.ArtifactKey{key}, p.Native)
	raw, err := json.Marshal(p.Contribution)
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"content"`)
	require.Equal(t, []artifacts.Ref{first, second, external}, p.Contribution.Artifacts)
	other := second
	other.Stored = &artifacts.StoredRef{Key: key}
	other.Stored.Key.JobId = "different"
	for _, refs := range [][]artifacts.Ref{{first, first}, {first, other}, {{Kind: artifacts.RefKindAttached, Name: "a"}}, {{Kind: "bad", Name: "a"}}} {
		_, err := (Spec{true, map[string]any{"key": "k", "artifacts": refs}}).Prepare()
		require.Error(t, err)
	}
	for _, name := range []string{"/file", "../file", "a/../file", "a//b", "a\\b", "a\x00b", ".", ".."} {
		bad := external
		bad.Name = name
		_, err := (Spec{true, map[string]any{"key": "k", "artifacts": []artifacts.Ref{bad}}}).Prepare()
		require.Error(t, err, name)
	}
	external.Name = "nested/clean/file.txt"
	_, err = (Spec{true, map[string]any{"key": "k", "artifacts": []artifacts.Ref{external}}}).Prepare()
	require.NoError(t, err)
	p, err = (Spec{true, map[string]any{"key": "k", "content": map[string]any{}}}).Prepare()
	require.NoError(t, err)
	raw, err = json.Marshal(p.Contribution)
	require.NoError(t, err)
	require.JSONEq(t, `{"content":{},"artifacts":[]}`, string(raw))
}

func TestContributionJSONPreservesNativeNumbers(t *testing.T) {
	var restored Context
	require.NoError(t, json.Unmarshal([]byte(`{"appends":[{"content":{"integer":9007199254740993,"nested":[true,null,1.5]},"artifacts":[]},{"artifacts":[]}]}`), &restored))
	require.Equal(t, int64(9007199254740993), restored.Appends[0].Content["integer"])
	require.Equal(t, []any{true, nil, 1.5}, restored.Appends[0].Content["nested"])
	require.Nil(t, restored.Appends[1].Content)
}
