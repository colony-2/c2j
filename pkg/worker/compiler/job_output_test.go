package compiler

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/stretchr/testify/require"
)

// Job summaries contain measured durations. Business-output tests check their
// shape separately; metrics acceptance tests compare the actual totals with
// committed reports, terminal chapters and persisted inspection.
func recipeBusinessOutputs(t *testing.T, output map[string]any) map[string]any {
	t.Helper()
	require.Contains(t, output, metrics.JobOutputKey)
	raw, err := json.Marshal(output[metrics.JobOutputKey])
	require.NoError(t, err)
	var snapshot metrics.Snapshot
	require.NoError(t, json.Unmarshal(raw, &snapshot))
	require.Equal(t, metrics.Version, snapshot.Version)
	require.Equal(t, metrics.ReducerVersion, snapshot.ReducerVersion)
	require.NotNil(t, snapshot.Series)
	require.Contains(t, []string{"complete", "partial", "unavailable"}, snapshot.Coverage.History)
	result := maps.Clone(output)
	delete(result, metrics.JobOutputKey)
	return result
}

func requireJobOutputJSONEq(t *testing.T, expected string, raw []byte) {
	t.Helper()
	var output map[string]any
	require.NoError(t, json.Unmarshal(raw, &output))
	business, err := json.Marshal(recipeBusinessOutputs(t, output))
	require.NoError(t, err)
	require.JSONEq(t, expected, string(business))
}
