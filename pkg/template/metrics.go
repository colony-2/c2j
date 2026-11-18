package template

import (
	"fmt"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/google/cel-go/common/types"
)

func (rc *ResolutionContext) scopeMetadata() ScopeMetadata {
	rc.TemplateData.Scope.Metrics = rc.metricScope.Snapshot()
	return rc.TemplateData.Scope
}

// JSON alone cannot distinguish a float64 instrument's 1.0 from an int64 1.
// Restore the typed leaves after flattening, without changing business numbers
// or sharing mutable containers with a published snapshot.
func restoreMetricTemplateTypes(value any, out map[string]any) {
	switch v := value.(type) {
	case metrics.Snapshot:
		out["version"], out["reducer_version"], out["cutoff"] = v.Version, v.ReducerVersion, v.Cutoff
		series, _ := out["series"].(map[string]any)
		for name, points := range v.Series {
			items, _ := series[name].([]any)
			for i, point := range points {
				p := items[i].(map[string]any)
				if point.Value != nil {
					p["value"] = point.Value
				}
				if bounds, ok := p["bounds"].([]any); ok {
					for j, bound := range point.Bounds {
						bounds[j] = bound
					}
				}
				if point.Histogram != nil {
					h := p["histogram"].(map[string]any)
					h["count"], h["sum"] = point.Histogram.Count, point.Histogram.Sum
					if point.Histogram.Min != nil {
						h["min"], h["max"] = point.Histogram.Min, point.Histogram.Max
					}
					buckets := h["bucket_counts"].([]any)
					for j, count := range point.Histogram.BucketCounts {
						buckets[j] = count
					}
				}
				if point.Position != nil {
					position := p["position"].(map[string]any)
					position["attempt"], position["ordinal"], position["mutation"] = point.Position.Attempt, point.Position.Ordinal, point.Position.Mutation
				}
			}
		}
		coverage := out["coverage"].(map[string]any)
		for key, counts := range map[string]map[string]map[string]int{"reports": v.Coverage.Reports, "phases": v.Coverage.Phases} {
			groups, _ := coverage[key].(map[string]any)
			for group, statuses := range counts {
				values := groups[group].(map[string]any)
				for status, count := range statuses {
					values[status] = count
				}
			}
		}
		cpu := coverage["cpu"].(map[string]any)
		cpu["process_seconds"], cpu["measured_seconds"] = v.Coverage.CPU.ProcessSeconds, v.Coverage.CPU.MeasuredSeconds
		if excluded, ok := cpu["excluded_seconds"].(map[string]any); ok {
			for reason, seconds := range v.Coverage.CPU.ExcludedSeconds {
				excluded[reason] = seconds
			}
		}
	case contextual.TaskExecutionContext:
		out["metrics"] = flattenTemplateValue(v.Metrics)
	case ScopeMetadata:
		out["metrics"] = flattenTemplateValue(v.Metrics)
	case map[string]StepOutput:
		for key, step := range v {
			restoreMetricTemplateTypes(step, out[key].(map[string]any))
		}
	case StepOutput:
		out["metrics"] = flattenTemplateValue(v.Metrics)
		if runs, ok := out["runs"].([]any); ok {
			for i, run := range v.Runs {
				restoreMetricTemplateTypes(run, runs[i].(map[string]any))
			}
		}
	case RunOutput:
		out["metrics"] = flattenTemplateValue(v.Metrics)
	}
}

// Metric points have numeric leaves whose type depends on their descriptor.
// Expose the JSON projection as maps rather than CEL's native struct fields.
type metricTypeProvider struct{ types.Provider }

func (p metricTypeProvider) FindStructFieldType(name, field string) (*types.FieldType, bool) {
	if field != "metrics" || (name != "contextual.TaskExecutionContext" && name != "contextual.StepOutput" && name != "contextual.RunOutput") {
		return p.Provider.FindStructFieldType(name, field)
	}
	get := func(obj any) (any, error) {
		switch v := obj.(type) {
		case contextual.TaskExecutionContext:
			return flattenTemplateValue(v.Metrics), nil
		case *contextual.TaskExecutionContext:
			if v != nil {
				return flattenTemplateValue(v.Metrics), nil
			}
		case contextual.StepOutput:
			return flattenTemplateValue(v.Metrics), nil
		case *contextual.StepOutput:
			if v != nil {
				return flattenTemplateValue(v.Metrics), nil
			}
		case contextual.RunOutput:
			return flattenTemplateValue(v.Metrics), nil
		case *contextual.RunOutput:
			if v != nil {
				return flattenTemplateValue(v.Metrics), nil
			}
		case map[string]any:
			return v["metrics"], nil
		}
		return nil, fmt.Errorf("invalid metric context %T", obj)
	}
	return &types.FieldType{Type: types.NewMapType(types.StringType, types.DynType), GetFrom: get, IsSet: func(any) bool { return true }}, true
}
