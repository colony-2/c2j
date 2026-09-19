package execution

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDemandPayloadAndViews(t *testing.T) {
	b := Requirements{Resources: Resources{CPU: ptr("1000m"), Memory: ptr("2Gi")}}
	d, err := Initial(&b, "pinned", Requirements{Resources: Resources{Memory: ptr("16Gi")}})
	require.NoError(t, err)
	raw, err := PayloadWithDemand(json.RawMessage(`{"large":9007199254740993,"c2j":{"other":{"x":null}}}`), d)
	require.NoError(t, err)
	require.Contains(t, string(raw), `9007199254740993`)
	require.Contains(t, string(raw), `"x":null`)
	v := Inspect(nil, raw)
	require.Equal(t, "specified", v.Status)
	require.True(t, v.Published)
	require.Equal(t, "1", *v.Demand.Effective.Resources.CPU)
	require.Equal(t, "16Gi", *v.Demand.Effective.Resources.Memory)
	meta, err := json.Marshal(map[string]any{"execution": d})
	require.NoError(t, err)
	v = Inspect(meta, nil)
	require.Equal(t, "submission", v.Source)
	require.False(t, v.Published)
	v = Inspect(nil, nil)
	require.Equal(t, "unresolved", v.Status)
	f := Filter{Allocation: Allocation{SchemaVersion: 1, Resources: Resources{Memory: ptr("32Gi")}}, IncludeUnresolved: true}
	match, err := f.Match(v)
	require.NoError(t, err)
	require.True(t, match)
	for _, bad := range []string{`{"c2j":{"execution":null}}`, `{"c2j":{"execution":{"schema_version":99}}}`, `{"c2j":[]}`} {
		v := Inspect(nil, json.RawMessage(bad))
		require.NotEmpty(t, v.Diagnostic)
		_, err := f.Match(v)
		require.Error(t, err)
	}
	d.Effective.Resources.Memory = ptr("1Gi")
	require.Error(t, d.Validate())
}
