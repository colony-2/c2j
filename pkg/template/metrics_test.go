package template

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/stretchr/testify/require"
)

func TestMetricValidationViewsAndPatchIsolation(t *testing.T) {
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{}, contextual.JobContext{}, ResolutionOptions{Mode: ModeValidate, ClampSliceIndex: true})
	require.NoError(t, err)
	rc.TemplateData.Sequence["metrics"] = StepOutput{Metrics: metrics.Empty(0), Outputs: map[string]any{"metrics": "business"}, Runs: []RunOutput{{Metrics: metrics.Empty(0)}}}
	for _, expr := range []string{
		`${{ !("tokens" in context.metrics.series) }}`,
		`${{ !("tokens" in scope.metrics.series) }}`,
		`${{ !("tokens" in sequence.metrics.metrics.series) }}`,
		`${{ !("tokens" in sequence.metrics.runs[0].metrics.series) }}`,
		`${{ sequence.metrics.outputs.metrics == "business" }}`,
	} {
		v, err := rc.ResolveValueWithMode(expr, ModeInterpolation)
		require.NoError(t, err, expr)
		require.IsType(t, true, v, expr)
	}
	for _, expr := range []string{`${{ context.metrics.series }}`, `${{ scope.metrics.series }}`, `${{ sequence.metrics.metrics.series }}`} {
		value, err := rc.ResolveValueWithMode(expr, ModeInterpolation)
		require.NoError(t, err, expr)
		require.Empty(t, value, expr)
	}
	for _, raw := range []string{
		`[{"op":"add","path":"/context/metrics","value":{}}]`,
		`[{"op":"replace","path":"/sequence/metrics/metrics","value":{}}]`,
		`[{"op":"copy","from":"/sequence/metrics/metrics","path":"/sequence/metrics/outputs/copied"}]`,
	} {
		_, err := jobrestart.ParsePatch([]byte(raw))
		require.Error(t, err, raw)
	}
	patch, err := jobrestart.ParsePatch([]byte(`[{"op":"replace","path":"/sequence/metrics/outputs/metrics","value":"patched"}]`))
	require.NoError(t, err)
	require.NoError(t, rc.ApplyContextPatch(*patch))
	require.Equal(t, "patched", rc.TemplateData.Sequence["metrics"].Outputs["metrics"])
	require.Equal(t, metrics.Empty(0), rc.TemplateData.Sequence["metrics"].Metrics)
}

func TestMetricInspectionSnapshotUsesCurrentFrontier(t *testing.T) {
	a := metrics.NewAccumulator()
	scope, close := a.EnterScope()
	defer close()
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{}, contextual.JobContext{}, ResolutionOptions{MetricsJob: a.Job(), MetricsScope: scope})
	require.NoError(t, err)
	// No template expression has read scope.metrics before inspection.
	a.Consume(metrics.Record{Task: "a", Attempt: 1, Ordinal: 3, Channel: "producer", ChannelReport: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: metrics.Version, Mutations: []metrics.Mutation{{Descriptor: metrics.Descriptor{Name: "tokens", Kind: "counter", NumberType: "int64"}, Op: "add", Value: int64(7)}}}}})
	_, visible, err := rc.RestartContextSnapshot()
	require.NoError(t, err)
	jobMetrics := visible["context"].(map[string]any)["metrics"].(map[string]any)
	scopeMetrics := visible["scope"].(map[string]any)["metrics"].(map[string]any)
	require.Equal(t, jobMetrics, scopeMetrics)
	require.Equal(t, json.Number("3"), scopeMetrics["cutoff"])
	series := scopeMetrics["series"].(map[string]any)
	require.Equal(t, json.Number("7"), series["tokens"].([]any)[0].(map[string]any)["value"])
}

func TestMetricNumericTypesInTemplates(t *testing.T) {
	a := metrics.NewAccumulator()
	a.Consume(metrics.Record{Task: "a", Attempt: 1, Ordinal: 3, Channel: "producer", ChannelReport: metrics.Channel{Status: "valid", Report: &metrics.Report{Version: metrics.Version, Mutations: []metrics.Mutation{
		{Descriptor: metrics.Descriptor{Name: "level", Kind: "gauge", NumberType: "float64"}, Op: "set", Value: float64(1)},
		{Descriptor: metrics.Descriptor{Name: "latency", Kind: "histogram", NumberType: "float64", Bounds: []float64{2}}, Op: "observe", Value: float64(2)},
		{Descriptor: metrics.Descriptor{Name: "tokens", Kind: "counter", NumberType: "int64"}, Op: "add", Value: int64(9007199254740993)},
	}}}})
	rc, err := NewRecipeResolutionContext(&contextual.GitCommitContext{}, map[string]any{}, contextual.JobContext{}, ResolutionOptions{MetricsJob: a.Job(), MetricsScope: a.Job()})
	require.NoError(t, err)
	rc.TemplateData.Sequence["work"] = StepOutput{Metrics: a.Job().Snapshot(), Runs: []RunOutput{{Metrics: a.Job().Snapshot()}}}
	for _, path := range []string{"context.metrics", "scope.metrics", "sequence.work.metrics", "sequence.work.runs[0].metrics"} {
		for _, expr := range []string{
			`${{ ` + path + `.series["level"][0].value + 0.5 }}`,
			`${{ ` + path + `.series["latency"][0].histogram.sum / double(` + path + `.series["latency"][0].histogram.count) - 0.5 }}`,
		} {
			v, err := rc.ResolveValueWithMode(expr, ModeInterpolation)
			require.NoError(t, err, expr)
			require.Equal(t, float64(1.5), v, expr)
		}
		goPath := path
		if path == "sequence.work.runs[0].metrics" {
			goPath = "(index sequence.work.runs 0).metrics"
		}
		v, err := rc.ResolveValueWithMode(`{{ eq (index `+goPath+`.series.tokens 0).value 9007199254740993 }}`, ModeInterpolation)
		require.NoError(t, err, path)
		require.Equal(t, "true", v, path)
		v, err = rc.ResolveValueWithMode(`{{ eq (index `+goPath+`.series.level 0).value 1.0 }}`, ModeInterpolation)
		require.NoError(t, err, path)
		require.Equal(t, "true", v, path)
	}
}
