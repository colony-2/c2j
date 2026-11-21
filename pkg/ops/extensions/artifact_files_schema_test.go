package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/stretchr/testify/require"
)

func TestArtifactFileSchemaBranchConflicts(t *testing.T) {
	branches := map[string]func(map[string]any) map[string]any{}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		branches[keyword] = func(child map[string]any) map[string]any {
			return map[string]any{keyword: []any{child}}
		}
	}
	for _, keyword := range []string{"not", "if", "then", "else"} {
		branches[keyword] = func(child map[string]any) map[string]any {
			return map[string]any{keyword: child}
		}
	}
	for _, keyword := range []string{"dependentSchemas", "dependencies"} {
		branches[keyword] = func(child map[string]any) map[string]any {
			return map[string]any{keyword: map[string]any{"kind": child}}
		}
	}
	for branch, wrap := range branches {
		for annotation, value := range map[string]any{"x-c2j-object-type": "example.session/v1", "x-secret": true, "$ref": "#/$defs/x", "$dynamicRef": "#x"} {
			t.Run(branch+"/"+annotation, func(t *testing.T) {
				property := fileSchemaProperty()
				// Nested branches must be checked too.
				for k, v := range wrap(map[string]any{"allOf": []any{map[string]any{annotation: value}}}) {
					property[k] = v
				}
				schema := map[string]any{"type": "object", "properties": map[string]any{"instructions_file": property}}
				_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
				require.ErrorContains(t, err, "input_schema.properties.instructions_file."+branch)
				require.ErrorContains(t, err, "conflicts with secret, checkpoint or reference")
				// Exercise source manifest loading, not just restored metadata.
				root := t.TempDir()
				raw, err := json.Marshal(map[string]any{"name": "conflict", "command": []string{"bin/consumer"}, "input_schema": schema, "output_schema": map[string]any{"type": "object"}})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(root, "op.yaml"), raw, 0600))
				_, err = Resolve(context.Background(), "./", ResolveOptions{BaseDir: root})
				require.ErrorContains(t, err, "conflicts with secret, checkpoint or reference")
			})
		}
	}
}

func TestArtifactFileSchemaEnclosingBranchConflicts(t *testing.T) {
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "dependentSchemas", "dependencies"} {
		for annotation, value := range map[string]any{"x-c2j-object-type": "example.session/v1", "x-secret": true, "$ref": "#/$defs/x", "$dynamicRef": "#x"} {
			t.Run(keyword+"/"+annotation, func(t *testing.T) {
				declaration := map[string]any{"properties": map[string]any{"instructions_file": map[string]any{"allOf": []any{map[string]any{annotation: value}}}}}
				var branch any = declaration
				switch keyword {
				case "allOf", "anyOf", "oneOf":
					branch = []any{declaration}
				case "dependentSchemas", "dependencies":
					branch = map[string]any{"kind": declaration}
				}
				schema := fileInputSchema()
				// The extra allOf exercises multiple enclosing composition levels.
				schema["allOf"] = []any{map[string]any{keyword: branch}}
				assertArtifactFileManifestConflict(t, schema, "input_schema.allOf[0]."+keyword)
			})
		}
	}
}

func assertArtifactFileManifestConflict(t *testing.T, schema map[string]any, path string) {
	t.Helper()
	_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
	require.ErrorContains(t, err, path)
	require.ErrorContains(t, err, "conflicts with secret, checkpoint or reference")
	root := t.TempDir()
	raw, err := json.Marshal(map[string]any{"name": "conflict", "command": []string{"bin/consumer"}, "input_schema": schema, "output_schema": map[string]any{"type": "object"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "op.yaml"), raw, 0600))
	_, err = Resolve(context.Background(), "./", ResolveOptions{BaseDir: root})
	require.ErrorContains(t, err, path)
	require.ErrorContains(t, err, "conflicts with secret, checkpoint or reference")
}

func TestArtifactFileSchemaOverlappingDeclarations(t *testing.T) {
	checkpoint := map[string]any{"x-c2j-object-type": "example.session/v1"}
	for name, branch := range map[string]map[string]any{
		"patternProperties":     {"patternProperties": map[string]any{"^instructions_": checkpoint}},
		"additionalProperties":  {"additionalProperties": checkpoint},
		"unevaluatedProperties": {"unevaluatedProperties": checkpoint},
		"reference":             {"$ref": "#/$defs/elsewhere"},
	} {
		t.Run(name, func(t *testing.T) {
			schema := fileInputSchema()
			schema["allOf"] = []any{branch}
			assertArtifactFileManifestConflict(t, schema, "input_schema.allOf[0]")
		})
	}
}

func TestArtifactFileSchemaUnrelatedEnclosingDeclarations(t *testing.T) {
	checkpoint := map[string]any{"x-c2j-object-type": "example.session/v1"}
	for name, branch := range map[string]map[string]any{
		"sibling":              {"properties": map[string]any{"session": checkpoint}},
		"child":                {"properties": map[string]any{"nested": map[string]any{"properties": map[string]any{"instructions_file": checkpoint}}}},
		"definition":           {"$defs": map[string]any{"instructions_file": checkpoint}},
		"examples":             {"examples": []any{map[string]any{"properties": map[string]any{"instructions_file": checkpoint}}}},
		"default":              {"default": map[string]any{"properties": map[string]any{"instructions_file": checkpoint}}},
		"nonmatching pattern":  {"patternProperties": map[string]any{"^other_": checkpoint}},
		"ordinary declaration": {"properties": map[string]any{"instructions_file": map[string]any{"type": "object"}}},
	} {
		t.Run(name, func(t *testing.T) {
			schema := fileInputSchema()
			schema["allOf"] = []any{branch}
			// Root additionalProperties does not apply to explicitly named files.
			schema["additionalProperties"] = checkpoint
			_, err := artifactFileProperties(schema, nil)
			require.NoError(t, err)
			if name == "default" {
				return // Defaults in compositions are independently unsupported.
			}
			op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
			require.NoError(t, err)
			ref := artifacts.NewExternalRef("x", "file:///tmp/x", false)
			require.NoError(t, op.ValidateInvocationInputs(map[string]any{"instructions_file": ref}))
			require.NoError(t, op.ValidateMaterializedInputs(map[string]any{"instructions_file": ref}))
		})
	}
}

func TestArtifactFileSchemaUnrelatedHandlers(t *testing.T) {
	checkpoint := map[string]any{"x-c2j-object-type": "example.session/v1"}
	for _, keyword := range []string{"properties", "patternProperties", "$defs", "definitions", "items", "contains", "additionalProperties", "prefixItems", "default", "examples"} {
		t.Run(keyword, func(t *testing.T) {
			property := fileSchemaProperty()
			switch keyword {
			case "properties", "patternProperties", "$defs", "definitions":
				property[keyword] = map[string]any{"child": checkpoint}
			case "prefixItems", "examples":
				property[keyword] = []any{checkpoint}
			default:
				property[keyword] = checkpoint
			}
			schema := map[string]any{"type": "object", "properties": map[string]any{"instructions_file": property}}
			_, err := artifactFileProperties(schema, nil)
			require.NoError(t, err)
			if keyword == "default" {
				return // Default reference shape is validated separately from handler discovery.
			}
			_, err = RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
			require.NoError(t, err, "other instances and annotation-looking data must remain legal")
		})
	}
}

func fileSchemaProperty() map[string]any {
	return map[string]any{"type": "object", artifactFileAnnotation: true}
}
func fileInputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"instructions_file": fileSchemaProperty(), "prompt_prefix_file": fileSchemaProperty()}}
}

func TestArtifactFileSchemaRestoreAndValidation(t *testing.T) {
	schema := fileInputSchema()
	schema["required"] = []string{"instructions_file"}
	op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
	require.NoError(t, err)
	raw, err := json.Marshal(op)
	require.NoError(t, err)
	var saved ResolvedOp
	require.NoError(t, json.Unmarshal(raw, &saved))
	op, err = RestoreResolvedOp(&saved)
	require.NoError(t, err)
	require.Equal(t, []string{"instructions_file", "prompt_prefix_file"}, op.artifactFiles)
	require.Error(t, op.ValidateInvocationInputs(map[string]any{}))
	for _, v := range []any{nil, "/tmp/x", map[string]any{}, map[string]any{"ref": artifacts.NewExternalRef("x", "file:///tmp/x", false), "path": "/tmp/x"}} {
		require.ErrorContains(t, op.ValidateInvocationInputs(map[string]any{"instructions_file": v}), "inputs.instructions_file")
	}
	ref := artifacts.NewExternalRef("x", "file:///tmp/x", false)
	require.NoError(t, op.ValidateInvocationInputs(map[string]any{"instructions_file": ref}))
	require.NoError(t, op.ValidateMaterializedInputs(map[string]any{"instructions_file": ref}))
	require.NotContains(t, string(raw), "anyOf", "expansion must not alter durable manifest")
}

func TestArtifactFileSchemaLocations(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema map[string]any
		output bool
	}{
		{"root", fileSchemaProperty(), false},
		{"output", fileInputSchema(), true},
		{"nested", map[string]any{"properties": map[string]any{"outer": fileInputSchema()}}, false},
		{"definition", map[string]any{"$defs": map[string]any{"x": fileSchemaProperty()}}, false},
		{"branch", map[string]any{"anyOf": []any{fileInputSchema()}}, false},
		{"items", map[string]any{"type": "array", "items": fileSchemaProperty()}, false},
		{"legacy items", map[string]any{"items": []any{fileSchemaProperty()}}, false},
		{"additionalProperties", map[string]any{"additionalProperties": fileSchemaProperty()}, false},
		{"dependencies", map[string]any{"dependencies": map[string]any{"x": fileSchemaProperty()}}, false},
		{"contentSchema", map[string]any{"contentSchema": fileSchemaProperty()}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := ExtensionOpSpec{InputSchema: test.schema}
			if test.output {
				spec.OutputSchema = test.schema
				spec.InputSchema = nil
			}
			_, err := RestoreResolvedOp(&ResolvedOp{Spec: spec})
			require.ErrorContains(t, err, artifactFileAnnotation)
		})
	}
	for _, v := range []any{false, "true", 1, nil, map[string]any{}} {
		s := fileInputSchema()
		s["properties"].(map[string]any)["instructions_file"].(map[string]any)[artifactFileAnnotation] = v
		_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: s}})
		require.ErrorContains(t, err, "must be boolean true")
	}
	for _, conflict := range []map[string]any{{"type": "string"}, {"x-secret": true}, {"x-c2j-object-type": "example.session/v1"}, {"$ref": "#/$defs/x"}} {
		s := fileInputSchema()
		p := s["properties"].(map[string]any)["instructions_file"].(map[string]any)
		for k, v := range conflict {
			p[k] = v
		}
		_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: s}})
		require.Error(t, err)
	}
	// Annotation-looking data and property names are not declarations.
	s := map[string]any{"type": "object", "default": map[string]any{artifactFileAnnotation: false}, "examples": []any{map[string]any{artifactFileAnnotation: "data"}}, "properties": map[string]any{artifactFileAnnotation: map[string]any{"type": "boolean"}}}
	_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: s}})
	require.NoError(t, err)
}
