package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Native adaptation of the supplied c2ops model/reasoning regression matrix.
// API defaults are suppressed to isolate provider-call and override assertions.
func TestCodexConfigurationDefaultsMatrix(t *testing.T) {
	for _, manifest := range []string{"op.json", "op.yaml", "run_skill/op.json", "run_skill/op.yaml"} {
		t.Run(manifest, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/codex-defaults/" + manifest)
			require.NoError(t, err)
			var spec ExtensionOpSpec
			require.NoError(t, yaml.Unmarshal(raw, &spec))
			op, err := RestoreResolvedOp(&ResolvedOp{Spec: spec})
			require.NoError(t, err)
			// Restore the durable schema as well as each durable invocation below.
			raw, err = json.Marshal(op)
			require.NoError(t, err)
			var saved ResolvedOp
			require.NoError(t, json.Unmarshal(raw, &saved))
			op, err = RestoreResolvedOp(&saved)
			require.NoError(t, err)
			base := func() map[string]any {
				in := map[string]any{"prompt": "test", "api": nil}
				if strings.Contains(manifest, "run_skill") {
					in["skill"] = "test"
				}
				return in
			}
			materialize := func(t *testing.T, in map[string]any, values map[string]json.RawMessage, wantCalls int) map[string]any {
				t.Helper()
				_, err := op.ApplyInvocationDefaults(in)
				require.NoError(t, err)
				wire, err := json.Marshal(in)
				require.NoError(t, err)
				var restored map[string]any
				require.NoError(t, json.Unmarshal(wire, &restored))
				calls := 0
				out, _, err := configuration.MaterializeInput(context.Background(), nil, restored, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
					calls++
					return jobdb.ConfigurationSnapshot{Values: values}, nil
				})
				require.NoError(t, err)
				require.Equal(t, wantCalls, calls)
				return out.(map[string]any)
			}
			for _, field := range []string{"model", "reasoning_effort"} {
				other := "model"
				if field == other {
					other = "reasoning_effort"
				}
				for _, setting := range []string{"", "null", `""`, `" \t\n"`, `" opaque-value "`, `false`, `42`, `{}`, `[]`, `[null]`} {
					for _, explicit := range []string{"omitted", "", "override", "null"} {
						t.Run(field+"/"+setting+"/"+explicit, func(t *testing.T) {
							in := base()
							in[other] = ""
							if explicit == "null" {
								in[field] = nil
							} else if explicit != "omitted" {
								in[field] = explicit
							}
							values := map[string]json.RawMessage{}
							if setting != "" {
								values["codex_"+field] = json.RawMessage(setting)
							}
							calls := 0
							if explicit == "omitted" {
								calls = 1
							}
							out := materialize(t, in, values, calls)
							invalid := explicit == "null" || (explicit == "omitted" && (setting == "false" || setting == "42" || setting == "{}" || strings.HasPrefix(setting, "[")))
							err := op.ValidateMaterializedInputs(out)
							if invalid {
								require.Error(t, err)
								if explicit == "null" {
									require.Contains(t, out, field)
								}
								return
							}
							require.NoError(t, err)
							if explicit == "omitted" && (setting == "" || setting == "null") {
								require.NotContains(t, out, field)
							} else {
								var want any = explicit
								if explicit == "omitted" {
									require.NoError(t, json.Unmarshal([]byte(setting), &want))
								}
								require.Equal(t, want, out[field])
							}
						})
					}
				}
			}
			for _, a := range []string{"", "null", `"model-value"`} {
				for _, b := range []string{"", "null", `"effort-value"`} {
					t.Run("joint/"+a+"/"+b, func(t *testing.T) {
						values := map[string]json.RawMessage{}
						if a != "" {
							values["codex_model"] = json.RawMessage(a)
						}
						if b != "" {
							values["codex_reasoning_effort"] = json.RawMessage(b)
						}
						out := materialize(t, base(), values, 1)
						require.NoError(t, op.ValidateMaterializedInputs(out))
						for field, setting := range map[string]string{"model": a, "reasoning_effort": b} {
							if setting == "" || setting == "null" {
								require.NotContains(t, out, field)
							} else {
								var want string
								require.NoError(t, json.Unmarshal([]byte(setting), &want))
								require.Equal(t, want, out[field])
							}
						}
					})
				}
			}
			for _, failure := range []error{errors.New("provider outage"), context.Canceled, context.DeadlineExceeded, jobdb.ErrExecutionLeaseLost} {
				in := base()
				_, err := op.ApplyInvocationDefaults(in)
				require.NoError(t, err)
				_, _, err = configuration.MaterializeInput(context.Background(), nil, in, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
					return jobdb.ConfigurationSnapshot{}, failure
				})
				require.ErrorIs(t, err, failure)
			}
		})
	}
}

func TestConfigurationDefaultRequiredAndNested(t *testing.T) {
	for _, required := range []bool{false, true} {
		schema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string", "default": `${{ context.config.?value.orValue(null) }}`}}}
		if required {
			schema["required"] = []any{"value"}
		}
		op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
		require.NoError(t, err)
		for _, raw := range []string{"", "null", "false", "[]", `"valid"`} {
			in := map[string]any{}
			_, err := op.ApplyInvocationDefaults(in)
			require.NoError(t, err)
			out, _, err := configuration.MaterializeInput(context.Background(), nil, in, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				v := map[string]json.RawMessage{}
				if raw != "" {
					v["value"] = json.RawMessage(raw)
				}
				return jobdb.ConfigurationSnapshot{Values: v}, nil
			})
			require.NoError(t, err)
			err = op.ValidateMaterializedInputs(out.(map[string]any))
			if raw == "false" || raw == "[]" || (required && raw != `"valid"`) {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		}
	}
	// References nested in literal defaults must carry the child's nullability.
	schema := map[string]any{"type": "object", "properties": map[string]any{"nested": map[string]any{"type": "object", "default": map[string]any{"value": `${{ context.config.missing }}`}, "properties": map[string]any{"value": map[string]any{"type": "string"}}}}}
	op, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
	require.NoError(t, err)
	in := map[string]any{}
	_, err = op.ApplyInvocationDefaults(in)
	require.NoError(t, err)
	out, _, err := configuration.MaterializeInput(context.Background(), nil, in, func(context.Context) (jobdb.ConfigurationSnapshot, error) { return jobdb.ConfigurationSnapshot{}, nil })
	require.NoError(t, err)
	require.Equal(t, map[string]any{"nested": map[string]any{}}, out)
	require.NoError(t, op.ValidateMaterializedInputs(out.(map[string]any)))
}
