package template

import (
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/configuration"
)

func (rc *ResolutionContext) configScalar(text string) (any, bool, error) {
	var expr string
	if strings.HasPrefix(text, "${{") && strings.HasSuffix(text, "}}") {
		expr = strings.TrimSpace(text[3 : len(text)-2])
	} else if strings.HasPrefix(text, "{{") && strings.HasSuffix(text, "}}") {
		expr = strings.TrimSpace(text[2 : len(text)-2])
	}
	var value any
	if ref, ok := configuration.Expression(expr); ok {
		value = ref
	} else if strings.HasPrefix(expr, "inputs.") {
		// Only whole-value forwarding; CEL must never inspect reference internals.
		path := strings.Split(strings.TrimPrefix(expr, "inputs."), ".")
		var v any = rc.TemplateData.ContainerInputs
		for _, key := range path {
			if _, ref, _ := configuration.Parse(v); ref {
				return nil, true, fmt.Errorf("cannot inspect config reference internals")
			}
			m, ok := v.(map[string]any)
			if !ok {
				v = nil
				break
			}
			v = m[key]
		}
		if configuration.Contains(v) {
			value = v
		}
	}
	if value == nil {
		return nil, false, nil
	}
	if !rc.allowSecrets {
		return nil, true, fmt.Errorf("config references are only allowed in task inputs; return the value from an op before using it for recipe routing")
	}
	return value, true, nil
}
