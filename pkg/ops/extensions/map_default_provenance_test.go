package extensions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// The default is on a supported parent property, not on an additional/pattern
// schema. Every matching constraint governs the inserted entry's nullability.
func TestLiteralMapDefaultProvenance(t *testing.T) {
	stringSchema := func() map[string]any { return map[string]any{"type": "string"} }
	nullableSchema := func() map[string]any { return map[string]any{"type": []any{"string", "null"}} }
	for _, tc := range []struct {
		name        string
		constraints map[string]any
		nullable    bool
	}{
		{"additional", map[string]any{"additionalProperties": stringSchema()}, false},
		{"pattern", map[string]any{"patternProperties": map[string]any{"^value$": stringSchema()}}, false},
		{"overlap nullable then string", map[string]any{"patternProperties": map[string]any{"^v": nullableSchema(), "value$": stringSchema()}}, false},
		{"overlap string then nullable", map[string]any{"patternProperties": map[string]any{"^v": stringSchema(), "value$": nullableSchema()}}, false},
		{"named nullable with string pattern", map[string]any{"properties": map[string]any{"value": nullableSchema()}, "patternProperties": map[string]any{"^v": stringSchema()}}, false},
		{"named string with nullable pattern", map[string]any{"properties": map[string]any{"value": stringSchema()}, "patternProperties": map[string]any{"^v": nullableSchema()}}, false},
		{"nullable additional", map[string]any{"additionalProperties": nullableSchema()}, true},
		{"nullable patterns", map[string]any{"patternProperties": map[string]any{"^v": nullableSchema(), "value$": nullableSchema()}}, true},
		{"unmatched pattern uses additional", map[string]any{"patternProperties": map[string]any{"^other$": stringSchema()}, "additionalProperties": nullableSchema()}, true},
		{"named excludes additional", map[string]any{"properties": map[string]any{"value": nullableSchema()}, "additionalProperties": stringSchema()}, true},
		{"pattern excludes additional", map[string]any{"patternProperties": map[string]any{"^v": nullableSchema()}, "additionalProperties": stringSchema()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, required := range []bool{false, true} {
				nested := map[string]any{"type": "object", "default": map[string]any{"value": `${{ context.config.?setting.orValue(null) }}`}}
				for key, value := range tc.constraints {
					nested[key] = value
				}
				if required {
					nested["required"] = []any{"value"}
				}
				schema := map[string]any{"type": "object", "properties": map[string]any{"nested": nested}}
				op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
				require.NoError(t, err)
				wire, err := json.Marshal(op)
				require.NoError(t, err)
				var restored ResolvedOp
				require.NoError(t, json.Unmarshal(wire, &restored))
				op, err = RestoreResolvedOp(&restored)
				require.NoError(t, err)
				for _, setting := range []string{"missing", "null", `"configured"`, `false`, `[]`} {
					for _, invocation := range []string{"default", "explicit null", "explicit string", "explicit reference"} {
						name := setting + "/" + invocation
						if required {
							name = "required/" + name
						}
						t.Run(name, func(t *testing.T) {
							input := map[string]any{}
							switch invocation {
							case "explicit null":
								input["nested"] = map[string]any{"value": nil}
							case "explicit string":
								input["nested"] = map[string]any{"value": "caller"}
							case "explicit reference":
								input["nested"] = map[string]any{"value": configuration.Ref("setting")}
							}
							_, err := op.ApplyInvocationDefaults(input)
							require.NoError(t, err)
							wire, err := json.Marshal(input)
							require.NoError(t, err)
							var durable map[string]any
							require.NoError(t, json.Unmarshal(wire, &durable))
							if invocation == "default" || invocation == "explicit reference" {
								ref, ok, err := configuration.Parse(durable["nested"].(map[string]any)["value"])
								require.NoError(t, err)
								require.True(t, ok)
								require.Equal(t, invocation == "default" && !tc.nullable, ref.SchemaDefaultNonNullable != "")
							}
							calls := 0
							out, _, err := configuration.MaterializeInput(context.Background(), nil, durable, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
								calls++
								values := map[string]json.RawMessage{}
								if setting != "missing" {
									values["setting"] = json.RawMessage(setting)
								}
								return jobdb.ConfigurationSnapshot{Values: values}, nil
							})
							require.NoError(t, err)
							if invocation == "explicit null" || invocation == "explicit string" {
								require.Zero(t, calls)
							} else {
								require.Equal(t, 1, calls)
							}
							entries := out.(map[string]any)["nested"].(map[string]any)
							resolvedNull := setting == "missing" || setting == "null"
							omitted := invocation == "default" && resolvedNull && !tc.nullable
							if omitted {
								require.NotContains(t, entries, "value")
							} else {
								require.Contains(t, entries, "value")
								if invocation == "explicit null" || ((invocation == "default" || invocation == "explicit reference") && resolvedNull) {
									require.Nil(t, entries["value"])
								}
								if invocation == "explicit string" {
									require.Equal(t, "caller", entries["value"])
								}
								if (invocation == "default" || invocation == "explicit reference") && setting == `"configured"` {
									require.Equal(t, "configured", entries["value"])
								}
							}
							invalid := omitted && required
							if invocation == "explicit null" {
								invalid = !tc.nullable
							}
							if invocation == "default" || invocation == "explicit reference" {
								if setting == "false" || setting == "[]" {
									invalid = true
								}
								if invocation == "explicit reference" && resolvedNull {
									invalid = !tc.nullable
								}
							}
							err = op.ValidateMaterializedInputs(out.(map[string]any))
							if invalid {
								require.Error(t, err)
							} else {
								require.NoError(t, err)
							}
						})
					}
				}
			}
		})
	}
}

func TestMapConstraintsStillRejectUnsupportedDefaults(t *testing.T) {
	for _, keyword := range []string{"additionalProperties", "patternProperties"} {
		child := map[string]any{"type": "string", "default": "forbidden"}
		var constraint any = child
		if keyword == "patternProperties" {
			constraint = map[string]any{"^value$": child}
		}
		schema := map[string]any{"type": "object", "properties": map[string]any{"nested": map[string]any{"type": "object", "default": map[string]any{}, keyword: constraint}}}
		_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
		require.ErrorContains(t, err, "unsupported schema location")
	}
}

func TestLiteralMapDefaultNestedContracts(t *testing.T) {
	for _, array := range []bool{false, true} {
		leaf := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
		var item any = map[string]any{"value": `${{ context.config.?missing.orValue(null) }}`}
		var want any = map[string]any{}
		if array {
			leaf = map[string]any{"type": "array", "items": leaf}
			item = []any{item}
			want = []any{want}
		}
		nested := map[string]any{"type": "object", "default": map[string]any{"entry": item}, "patternProperties": map[string]any{
			"^entry$": leaf,
			// An overlapping contract recursively allows null. It must not erase the
			// inner additionalProperties string contract from the first pattern.
			"entry": map[string]any{},
		}}
		schema := map[string]any{"type": "object", "properties": map[string]any{"nested": nested}}
		op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
		require.NoError(t, err)
		input := map[string]any{}
		_, err = op.ApplyInvocationDefaults(input)
		require.NoError(t, err)
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		var restored map[string]any
		require.NoError(t, json.Unmarshal(raw, &restored))
		out, _, err := configuration.MaterializeInput(context.Background(), nil, restored, func(context.Context) (jobdb.ConfigurationSnapshot, error) { return jobdb.ConfigurationSnapshot{}, nil })
		require.NoError(t, err)
		require.Equal(t, map[string]any{"nested": map[string]any{"entry": want}}, out)
		require.NoError(t, op.ValidateMaterializedInputs(out.(map[string]any)))
	}
}
