package template

import (
	"fmt"
	"strconv"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/recipe"
)

// ResolveExecutionNeeds uses the same resolver as node inputs, after local vars
// are available. Each context owns its overlay; leaving it restores its parent.
func (rc *ResolutionContext) ResolveExecutionNeeds(needs *recipe.ExecutionNeeds) error {
	if needs == nil {
		return nil
	}
	patch := execution.Requirements{}
	for _, field := range []struct {
		name   string
		value  any
		target **string
	}{
		{"resources.cpu", needs.Resources.CPU, &patch.Resources.CPU},
		{"resources.memory", needs.Resources.Memory, &patch.Resources.Memory},
		{"resources.ephemeral-storage", needs.Resources.EphemeralStorage, &patch.Resources.EphemeralStorage},
		{"image", needs.Image, &patch.Image}, {"platform", needs.Platform, &patch.Platform},
	} {
		if field.value == nil {
			continue
		}
		v, err := rc.ResolveValueWithMode(field.value, ModeInterpolation)
		if err != nil {
			return fmt.Errorf("node %q execution_needs.%s: %w", rc.scopeId, field.name, err)
		}
		if v == nil && rc.Options.Mode == ModeValidate {
			continue
		}
		var s string
		switch value := v.(type) {
		case string:
			s = value
		case int, int64, uint64:
			if field.name == "resources.cpu" {
				s = fmt.Sprint(value)
			}
		case float64:
			if field.name == "resources.cpu" {
				s = strconv.FormatFloat(value, 'f', -1, 64)
			}
		}
		if s == "" {
			return fmt.Errorf("node %q execution_needs.%s: expected a nonempty string (CPU also accepts a number)", rc.scopeId, field.name)
		}
		*field.target = &s
	}
	patch, err := patch.Normalize()
	if err != nil {
		return fmt.Errorf("node %q execution_needs: %w", rc.scopeId, err)
	}
	rc.ExecutionNeeds = execution.Overlay(rc.ExecutionNeeds, patch)
	return nil
}
