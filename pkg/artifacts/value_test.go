package artifacts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestValueSerialization(t *testing.T) {
	for _, tc := range []struct {
		format string
		value  any
		want   string
	}{
		{"text", " λ\n${{ missing.value }}\n$(exit 1) ' \" ", " λ\n${{ missing.value }}\n$(exit 1) ' \" "},
		{"text", "", ""}, {"json", nil, "null\n"}, {"json", "", "\"\"\n"},
		{"json", []any{}, "[]\n"}, {"json", map[string]any{}, "{}\n"},
		{"json", map[string]any{"z": true, "a": json.Number("9007199254740993")}, "{\"a\":9007199254740993,\"z\":true}\n"},
	} {
		r, ok, err := ParseValue(map[string]any{"kind": "value", "format": tc.format, "value": tc.value})
		require.True(t, ok)
		require.NoError(t, err)
		data, err := r.ValueBytes()
		require.NoError(t, err)
		require.Equal(t, tc.want, string(data))
		raw, err := json.Marshal(r)
		require.NoError(t, err)
		var roundTrip Ref
		require.NoError(t, json.Unmarshal(raw, &roundTrip))
		again, err := roundTrip.ValueBytes()
		require.NoError(t, err)
		require.Equal(t, data, again)
	}
	ref := NewStoredRef(jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 1, Name: "original", SizeBytes: 12})
	r, _, err := ParseValue(map[string]any{"kind": "value", "format": "json", "value": ref})
	require.NoError(t, err)
	data, err := r.ValueBytes()
	require.NoError(t, err)
	var got Ref
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, ref, got)
}

func TestValueDescriptorValidation(t *testing.T) {
	for _, bad := range []map[string]any{
		{"kind": "value", "format": "text"}, {"kind": "value", "value": "x"},
		{"kind": "value", "format": "yaml", "value": "x"},
		{"kind": "value", "format": "text", "value": nil}, {"kind": "value", "format": "text", "value": 42},
		{"kind": "value", "format": "json", "value": make(chan int)},
		{"kind": "value", "format": "json", "value": nil, "typo": true},
		{"kind": "value", "format": "json", "value": nil, "stored": map[string]any{}},
		{"kind": "value", "format": "json", "value": nil, "external": map[string]any{}},
	} {
		_, ok, err := ParseValue(bad)
		require.True(t, ok)
		require.Error(t, err)
	}
	_, ok, err := ParseValue(map[string]any{"kind": map[string]any{}, "value": "file named kind"})
	require.False(t, ok)
	require.NoError(t, err)
	r := NewExternalRef("x", "file:///tmp/x", false)
	r.Format = "json"
	require.Error(t, r.Validate())
	r, _, err = ParseValue(map[string]any{"kind": "value", "format": "text", "value": strings.Repeat("private", 100)})
	require.NoError(t, err)
	require.NotContains(t, r.Identity(), "private")
}
