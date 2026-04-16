package recipe

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	rops "github.com/colony-2/c2j/recipe-core/pkg/ops"
	"github.com/invopop/jsonschema"
	yamlv3 "gopkg.in/yaml.v3"
)

// wrapperInput provides per-op JSON Schema and YAML validation.
type wrapperInput struct {
	Seen bool                   `yaml:"-"`
	Data map[string]interface{} `yaml:",inline"`
}

// JSONSchema returns this op's input schema: requires a string "foo".
func (w wrapperInput) JSONSchema() *jsonschema.Schema {
	s := &jsonschema.Schema{Type: "object"}
	s.Properties = jsonschema.NewProperties()
	s.Properties.Set("foo", &jsonschema.Schema{Type: "string"})
	s.Required = append(s.Required, "foo")
	return s
}

// UnmarshalYAML validates against the same schema semantics at parse-time.
func (w *wrapperInput) UnmarshalYAML(unmarshal func(any) error) error {
	var m map[string]interface{}
	if err := unmarshal(&m); err != nil {
		return err
	}
	// Enforce required "foo" key
	if _, ok := m["foo"]; !ok {
		return fmt.Errorf("missing required field: foo")
	}
	w.Seen = true
	w.Data = m
	return nil
}

func TestProvider_SchemaReflection_And_YAMLValidation(t *testing.T) {
	const opName = "provider-op"

	// Reset registry; register a provider-based op
	rops.Clear()
	md := rops.OpMetadata{
		Type:        opName,
		Description: "provider-based test op",
		Version:     "1.0.0",
	}
	handler := func(_ rops.OpDependencies, _ context.Context, in map[string]interface{}) (map[string]interface{}, error) {
		// Echo input; runtime execution not under test here.
		return in, nil
	}
	// Use the new provider constructor to supply our wrapper input
	op := rops.NewActivityMappedOpV2[wrapperInput, map[string]interface{}](md, func(_ rops.OpDependencies, ctx context.Context, in wrapperInput) (map[string]interface{}, error) {
		return handler(nil, ctx, in.Data)
	})
	rops.Register(op)

	// Sanity: provider influences input type
	got, ok := rops.Get(opName)
	if !ok {
		t.Fatalf("op not registered: %s", opName)
	}
	chain := got.TaskChain()
	if len(chain) == 0 {
		t.Fatalf("expected at least one step")
	}
	if chain[0].InputType.Kind() != reflect.Struct {
		t.Fatalf("expected struct kind for input type, got: %s", chain[0].InputType.Kind())
	}

	// 1) Schema includes our op and its input shape (requires "foo")
	schemaStr, err := GenerateSchemaString()
	if err != nil {
		t.Fatalf("GenerateSchemaString error: %v", err)
	}
	if !strings.Contains(schemaStr, opName) {
		t.Fatalf("schema does not mention op name %q", opName)
	}
	if !strings.Contains(schemaStr, "\"foo\"") {
		t.Fatalf("schema does not include expected property %q", "foo")
	}

	// 2) YAML parse-time validation triggers wrapper's UnmarshalYAML
	var n Node

	// Success case: has required "foo"
	yOK := []byte("\nop: provider-op\ninputs:\n  foo: bar\n")
	if err := yamlv3.Unmarshal(yOK, &n); err != nil {
		t.Fatalf("expected no error for valid YAML, got: %v", err)
	}

	// Failure case: missing required "foo"
	yBad := []byte("\nop: provider-op\ninputs:\n  nope: nope\n")
	if err := yamlv3.Unmarshal(yBad, &n); err == nil {
		t.Fatalf("expected error for invalid YAML (missing foo), got nil")
	}
}
