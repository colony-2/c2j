package template

import (
	"fmt"
	"maps"
	"slices"

	"github.com/google/cel-go/common/types"
)

// ResolveOptionalInputs filters absent keys before converting present CEL values.
func (rc *ResolutionContext) ResolveOptionalInputs(expressions map[string]string) (map[string]any, error) {
	present := make(map[string]any, len(expressions))
	for _, key := range slices.Sorted(maps.Keys(expressions)) {
		value, err := rc.resolveOptionalInput(expressions[key])
		if err != nil {
			return nil, fmt.Errorf("optional_inputs.%s: %w", key, err)
		}
		if value.HasValue() {
			present[key] = nativeTemplateResult(value.GetValue())
		}
	}
	return present, nil
}

func (rc *ResolutionContext) resolveOptionalInput(text string) (*types.Optional, error) {
	segments, err := parseTemplate(text)
	if err != nil {
		return nil, err
	}
	if len(segments) != 1 {
		return nil, fmt.Errorf("expected a single CEL expression")
	}
	expr, ok := segments[0].(ExpressionSegment)
	if !ok {
		return nil, fmt.Errorf("expected a single CEL expression")
	}
	value, err := rc.evaluateCELValue(expr.Expression)
	if err != nil {
		return nil, err
	}
	optional, ok := value.(*types.Optional)
	if !ok {
		return nil, fmt.Errorf("expression must return a CEL optional")
	}
	return optional, nil
}
