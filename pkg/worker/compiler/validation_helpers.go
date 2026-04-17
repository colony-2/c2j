package compiler

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/core/artifacts"
	"github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/core/recipe"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/template"
)

func zeroOutputForOp(opName string) (map[string]interface{}, error) {
	if isSelectorOp(opName) {
		resolved, _, err := loadSelectorOp(opName, extops.ResolveOptions{})
		if err != nil {
			return nil, err
		}
		return resolved.ZeroOutput(), nil
	}
	registeredOp, exists := ops.Get(opName)
	if !exists {
		return nil, fmt.Errorf("operation %s not found", opName)
	}
	chain := registeredOp.TaskChain()
	if len(chain) == 0 {
		return map[string]interface{}{}, nil
	}
	return zeroOutputFromType(chain[len(chain)-1].OutputType)
}

func normalizeOpOutput(outputType reflect.Type, output map[string]interface{}) map[string]interface{} {
	if outputType == nil {
		return output
	}
	if outputType.Kind() == reflect.Pointer {
		outputType = outputType.Elem()
	}
	if outputType.Kind() != reflect.Struct {
		return output
	}
	base, err := zeroOutputFromType(outputType)
	if err != nil {
		return output
	}
	if output == nil {
		return base
	}
	for key, value := range output {
		base[key] = value
	}
	return base
}

func zeroOutputFromType(outputType reflect.Type) (map[string]interface{}, error) {
	if outputType == nil {
		return map[string]interface{}{}, nil
	}
	if outputType.Kind() == reflect.Pointer {
		outputType = outputType.Elem()
	}

	switch outputType.Kind() {
	case reflect.Struct:
		return zeroStructMap(outputType), nil
	case reflect.Map:
		return map[string]interface{}{}, nil
	default:
		return nil, fmt.Errorf("unsupported op output type: %s", outputType.Kind())
	}
}

func zeroOutputsFromTemplateMap(templateMap map[string]interface{}) map[string]interface{} {
	if templateMap == nil {
		return map[string]interface{}{}
	}
	out := make(map[string]interface{}, len(templateMap))
	for key, value := range templateMap {
		out[key] = zeroValueFromTemplate(value)
	}
	return out
}

func zeroValueFromTemplate(value interface{}) interface{} {
	switch v := value.(type) {
	case string:
		return ""
	case bool:
		return false
	case int, int32, int64, float32, float64:
		return 0
	case map[string]interface{}:
		nested := make(map[string]interface{}, len(v))
		for k, item := range v {
			nested[k] = zeroValueFromTemplate(item)
		}
		return nested
	case []interface{}:
		return []interface{}{}
	case nil:
		return nil
	default:
		return ""
	}
}

func zeroStructMap(outputType reflect.Type) map[string]interface{} {
	out := make(map[string]interface{}, outputType.NumField())
	for i := 0; i < outputType.NumField(); i++ {
		field := outputType.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := field.Tag.Get("json")
		if name == "-" {
			continue
		}
		if name != "" {
			if comma := strings.Index(name, ","); comma >= 0 {
				name = name[:comma]
			}
		}
		if name == "" {
			name = field.Name
		}
		out[name] = zeroValueForType(field.Type)
	}
	return out
}

func zeroValueForType(t reflect.Type) interface{} {
	if t.Kind() == reflect.Pointer {
		return zeroValueForType(t.Elem())
	}
	switch t.Kind() {
	case reflect.String:
		return ""
	case reflect.Bool:
		return false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 0
	case reflect.Float32, reflect.Float64:
		return 0
	case reflect.Map:
		return map[string]interface{}{}
	case reflect.Slice, reflect.Array:
		return []interface{}{}
	case reflect.Struct:
		return zeroStructMap(t)
	case reflect.Interface:
		return nil
	default:
		return nil
	}
}

func placeholderStepOutput(outputs map[string]interface{}) template.StepOutput {
	// Wrap dynamic outputs so CEL validation tolerates unknown keys (e.g., child recipe outputs).
	wrapped := make(map[string]interface{}, len(outputs))
	for k, v := range outputs {
		wrapped[k] = v
	}
	dyn := template.DynamicOutputs(wrapped)
	return template.StepOutput{
		Outputs:   dyn,
		Artifacts: map[string]recipeartifacts.Ref{},
		Runs: []template.RunOutput{
			{
				Outputs:   dyn,
				Artifacts: map[string]recipeartifacts.Ref{},
			},
		},
	}
}

func sortedStateNames(states map[string]recipe.State) []string {
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
