package cel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	"github.com/invopop/jsonschema"
	jsg "github.com/swaggest/jsonschema-go"
)

type CELExpr struct {
	expr    string
	program cel.Program
}

// IsZero enables correct `omitempty` behavior for YAML/JSON encoders.
// CELExpr stores its value in unexported fields, so reflection-based zero checks
// may incorrectly treat non-empty expressions as empty.
func (e CELExpr) IsZero() bool {
	return strings.TrimSpace(e.expr) == ""
}

func (e CELExpr) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.expr)
}

func (e *CELExpr) UnmarshalJSON(data []byte) error {
	// Treat null as empty expression for robustness.
	if string(data) == "null" {
		e.expr = ""
		e.program = nil
		return nil
	}

	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	// Intentionally do not compile during JSON unmarshal. Validation happens later.
	if strings.TrimSpace(s) == "" {
		e.expr = ""
		e.program = nil
		return nil
	}

	e.expr = s
	e.program = nil
	return nil
}

func (CELExpr) JSONSchema() (jsg.Schema, error) {
	var schema jsg.Schema
	schema.AddType(jsg.String)
	schema.WithDescription("cel expression")
	return schema, nil
}

func (e CELExpr) InlineJSONSchema() {
}

func (e CELExpr) String() string {
	return e.expr
}

func (e CELExpr) JSONSchemaExtend(schema *jsonschema.Schema) {
	schema.Type = "string"
	schema.Properties = nil
	schema.AdditionalProperties = nil
}

func (e CELExpr) AlwaysTrue() bool {
	return e.expr == "" || strings.ToLower(e.expr) == "true"
}

func NewCELExpr(expr string) (*CELExpr, error) {
	// Treat empty expressions as always-true conditions
	if strings.TrimSpace(expr) == "" {
		return &CELExpr{expr: "", program: nil}, nil
	}
	program, err := compile(expr)
	if err != nil {
		return nil, err
	}
	return &CELExpr{expr: expr, program: program}, nil
}

func (e CELExpr) AsBool(inputsMap map[string]interface{}) (bool, error) {
	if e.AlwaysTrue() {
		return true, nil
	}
	val, err := e.Evaluate(inputsMap)
	if err != nil {
		return false, err
	}

	b, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("expression '%s' did not evaluate to a boolean", e.expr)
	}
	return b, nil
}

func (e CELExpr) Evaluate(inputsMap map[string]interface{}) (interface{}, error) {
	if e.AlwaysTrue() {
		return true, nil
	}
	program := e.program
	if program == nil {
		var err error
		program, err = compile(e.expr)
		if err != nil {
			return nil, err
		}
	}
	val, _, err := program.Eval(map[string]interface{}{
		"inputs": &DynamicMapValue{data: inputsMap},
	})
	if err != nil {
		return nil, err
	}
	return val.Value(), nil
}

// MarshalYAML converts Duration to a YAML string
func (e CELExpr) MarshalYAML() (interface{}, error) {
	return e.expr, nil
}

func (e *CELExpr) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}

	// Intentionally do not compile during YAML unmarshal.
	// Validation happens later (e.g., during template validation / execution),
	// and compilation depends on the evaluation environment.
	if strings.TrimSpace(s) == "" {
		e.expr = ""
		e.program = nil
		return nil
	}

	e.expr = s
	e.program = nil
	return nil
}

// Evaluate evaluates a CEL expression with the given data
func compile(expression string) (cel.Program, error) {
	env, err := cel.NewEnv(
		cel.Variable("inputs", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("sequence", cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.DynType))),
		cel.Variable("states", cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.DynType))),
		cel.Variable("scope", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("context", cel.ObjectType("contextual.TaskExecutionContext")),
		ext.NativeTypes(
			reflect.TypeOf(contextual.StepOutput{}),
			reflect.TypeOf(contextual.RunOutput{}),
			reflect.TypeOf(contextual.TaskExecutionContext{}),
			reflect.TypeOf(contextual.JobContext{}),
			reflect.TypeOf(contextual.TaskContext{}),
			reflect.TypeOf(contextual.EnvironmentContext{}),
			reflect.TypeOf(contextual.EnvironmentPathContext{}),
			reflect.TypeOf(contextual.WorkflowContext{}),
			reflect.TypeOf(contextual.GitBaseContext{}),
			reflect.TypeOf(contextual.GitCommitContext{}),
			reflect.TypeOf(contextual.Invocation{}),
			reflect.TypeOf(recipeartifacts.Ref{}),
			reflect.TypeOf(recipeartifacts.StoredRef{}),
			reflect.TypeOf(recipeartifacts.ExternalRef{}),
			reflect.TypeOf(swf.ArtifactKey{}),
			ext.ParseStructTag("json"),
		),
	)

	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	// Parse expression
	ast, issues := env.Parse(expression)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to parse expression '%s': %w", expression, issues.Err())
	}

	// Check expression
	checked, issues := env.Check(ast)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to check expression '%s': %w", expression, issues.Err())
	}

	prg, err := env.Program(checked)
	if err != nil {
		return nil, fmt.Errorf("failed to create program for expression '%s': %w", expression, err)
	}

	// Return the native value
	return prg, nil
}

type DynamicMapValue struct {
	data map[string]interface{}
	path []string
}

// Type returns the CEL type - we use DynType since it's dynamic
func (d *DynamicMapValue) Type() ref.Type {
	return types.DynType
}

// Value returns the underlying Go value
func (d *DynamicMapValue) Value() interface{} {
	return d.data
}

// ConvertToNative converts to native Go type
func (d *DynamicMapValue) ConvertToNative(typeDesc reflect.Type) (interface{}, error) {
	if reflect.TypeOf(d.data).AssignableTo(typeDesc) {
		return d.data, nil
	}
	return nil, fmt.Errorf("cannot convert to %v", typeDesc)
}

// ConvertToType handles type conversions
func (d *DynamicMapValue) ConvertToType(typeVal ref.Type) ref.Val {
	switch typeVal {
	case types.MapType:
		return d // already a map-like type
	case types.DynType:
		return d
	case types.StringType:
		// Convert to JSON string representation
		if data, err := json.Marshal(d.data); err == nil {
			return types.String(string(data))
		}
	}
	return types.NewErr("cannot convert map to %v", typeVal)
}

// Equal checks equality
func (d *DynamicMapValue) Equal(other ref.Val) ref.Val {
	otherMap, ok := other.(*DynamicMapValue)
	if !ok {
		return types.False
	}
	// Deep equality check - you might want to optimize this
	return types.Bool(reflect.DeepEqual(d.data, otherMap.data))
}

// Get implements the critical property access - this is called for dot notation
func (d *DynamicMapValue) Get(key ref.Val) ref.Val {
	keyStr, ok := key.Value().(string)
	if !ok {
		return types.NewErr("key must be string")
	}

	val, exists := d.data[keyStr]
	if !exists {
		// Return null for missing keys (lenient mode)
		return types.NullValue
	}

	// Convert the value to appropriate CEL type
	return convertToCELValue(val, append(d.path, keyStr))
}

// Contains checks if key exists (used by 'has' function)
func (d *DynamicMapValue) Contains(key ref.Val) ref.Val {
	keyStr, ok := key.Value().(string)
	if !ok {
		return types.False
	}
	_, exists := d.data[keyStr]
	return types.Bool(exists)
}

// Size returns map size
func (d *DynamicMapValue) Size() ref.Val {
	return types.Int(len(d.data))
}

// Iterator for map traversal
func (d *DynamicMapValue) Iterator() traits.Iterator {
	keys := make([]string, 0, len(d.data))
	for k := range d.data {
		keys = append(keys, k)
	}
	return &mapIterator{
		keys:  keys,
		data:  d.data,
		index: 0,
	}
}

func convertToCELValue(val interface{}, path []string) ref.Val {
	if val == nil {
		return types.NullValue
	}

	switch v := val.(type) {
	case map[string]interface{}:
		// Nested map - return another DynamicMapValue
		return &DynamicMapValue{data: v, path: path}

	case []interface{}:
		// Convert slice to CEL list
		celList := make([]ref.Val, len(v))
		for i, item := range v {
			celList[i] = convertToCELValue(item, append(path, fmt.Sprintf("[%d]", i)))
		}
		return types.NewDynamicList(types.DefaultTypeAdapter, celList)

	case string:
		return types.String(v)

	case bool:
		return types.Bool(v)

	case int:
		return types.Int(v)

	case int32:
		return types.Int(v)

	case int64:
		return types.Int(v)

	case uint:
		return types.Uint(v)

	case uint32:
		return types.Uint(v)

	case uint64:
		return types.Uint(v)

	case float32:
		return types.Double(v)

	case float64:
		// Check if it's actually an integer
		if v == float64(int64(v)) {
			return types.Int(int64(v))
		}
		return types.Double(v)

	case []byte:
		return types.Bytes(v)

	default:
		// Fail fast with a clear error rather than panic
		return types.NewErr("unsupported type: %T", val)
	}
}

type mapIterator struct {
	keys  []string
	data  map[string]interface{}
	index int
}

// HasNext returns true if there are more elements
func (it *mapIterator) HasNext() ref.Val {
	return types.Bool(it.index < len(it.keys))
}

// Next returns the next element
// For CEL map iteration, it should return the key (not a key-value pair)
func (it *mapIterator) Next() ref.Val {
	if it.index >= len(it.keys) {
		return types.NewErr("no more elements")
	}

	key := it.keys[it.index]
	it.index++

	// Return just the key - CEL will call Get() to get the value if needed
	return types.String(key)
}

// Type returns the iterator type
func (it *mapIterator) Type() ref.Type {
	return types.IteratorType
}

// Value returns the raw value
func (it *mapIterator) Value() interface{} {
	return it
}

// ConvertToNative converts to native Go type
func (it *mapIterator) ConvertToNative(typeDesc reflect.Type) (interface{}, error) {
	return nil, fmt.Errorf("cannot convert iterator to native")
}

// ConvertToType converts to another CEL type
func (it *mapIterator) ConvertToType(typeVal ref.Type) ref.Val {
	if typeVal == types.IteratorType {
		return it
	}
	return types.NewErr("cannot convert iterator to %v", typeVal)
}

// Equal checks equality (iterators are not comparable)
func (it *mapIterator) Equal(other ref.Val) ref.Val {
	return types.False
}
