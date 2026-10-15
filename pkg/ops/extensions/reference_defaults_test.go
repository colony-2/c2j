package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func referenceDefaultSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"api": map[string]any{"type": []any{"object", "null"}, "additionalProperties": false, "properties": map[string]any{
			"base_url": map[string]any{"type": []any{"string", "null"}, "default": `${{ context.config.?base_url.orValue(null) }}`},
			"api_key":  map[string]any{"type": "string", "x-secret": true, "minLength": 1, "default": `${{ context.secrets.?api_key.orValue(null) }}`},
		}},
	}}
}

func TestReferenceDefaultBindingsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name        string
		input       map[string]any
		secretNames []string
		config      bool
	}{
		{"omitted", map[string]any{}, []string{"api_key"}, true},
		{"null", map[string]any{"api": nil}, nil, false},
		{"partial", map[string]any{"api": map[string]any{"base_url": "override"}}, []string{"api_key"}, false},
		{"caller reference", map[string]any{"api": map[string]any{"api_key": secrets.Reference{Name: "caller"}}}, []string{"caller"}, true},
		{"caller fallback", map[string]any{"api": map[string]any{"api_key": secrets.WithDefault(secrets.Reference{Name: "caller"}, "explicit")}}, []string{"caller"}, true},
		{"caller omission", map[string]any{"api": map[string]any{"api_key": secrets.Reference{Name: "caller", OmitIfUnavailable: true}}}, []string{"caller"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: referenceDefaultSchema()}})
			require.NoError(t, err)
			var original any
			if api, ok := tc.input["api"].(map[string]any); ok {
				original = api["api_key"]
			}
			_, err = r.ApplyInvocationDefaults(tc.input)
			require.NoError(t, err)
			names, err := secrets.Names(tc.input)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.secretNames, names)
			require.Equal(t, tc.config, configuration.Contains(tc.input))
			if original != nil {
				require.Equal(t, original, tc.input["api"].(map[string]any)["api_key"])
			}
			before, err := json.Marshal(tc.input)
			require.NoError(t, err)
			_, err = r.ApplyInvocationDefaults(tc.input)
			require.NoError(t, err)
			after, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "repeated preparation preserves bindings")
		})
	}
	// A secret default alone must synthesize its omitted parent.
	schema := referenceDefaultSchema()
	delete(schema["properties"].(map[string]any)["api"].(map[string]any)["properties"].(map[string]any), "base_url")
	r, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
	require.NoError(t, err)
	input := map[string]any{}
	_, err = r.ApplyInvocationDefaults(input)
	require.NoError(t, err)
	names, err := secrets.Names(input)
	require.NoError(t, err)
	require.Equal(t, []string{"api_key"}, names)
}

func TestReferenceDefaultsMatchExplicitBindings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string]jobdb.SecretValue
		config  map[string]json.RawMessage
		want    string
		invalid bool
	}{
		{name: "both", values: map[string]jobdb.SecretValue{"api_key": {Value: "credential"}}, config: map[string]json.RawMessage{"base_url": json.RawMessage(`"https://example.test"`)}, want: `{"api":{"base_url":"https://example.test","api_key":"credential"}}`},
		{name: "missing or denied", want: `{"api":{"base_url":null}}`},
		{name: "url only", config: map[string]json.RawMessage{"base_url": json.RawMessage(`"https://example.test"`)}, want: `{"api":{"base_url":"https://example.test"}}`},
		{name: "key only", values: map[string]jobdb.SecretValue{"api_key": {Value: "credential"}}, want: `{"api":{"base_url":null,"api_key":"credential"}}`},
		{name: "empty secret", values: map[string]jobdb.SecretValue{"api_key": {Value: ""}}, want: `{"api":{"base_url":null,"api_key":""}}`, invalid: true},
		{name: "existing null config", config: map[string]json.RawMessage{"base_url": json.RawMessage(`null`)}, want: `{"api":{"base_url":null}}`},
		{name: "wrong config type", config: map[string]json.RawMessage{"base_url": json.RawMessage(`false`)}, want: `{"api":{"base_url":false}}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: referenceDefaultSchema()}})
			require.NoError(t, err)
			secretRef, ok := secrets.Template(`${{ context.secrets.?api_key.orValue(null) }}`)
			require.True(t, ok)
			configRef, ok := configuration.Template(`${{ context.config.?base_url.orValue(null) }}`)
			require.True(t, ok)
			for _, input := range []map[string]any{{}, {"api": map[string]any{"api_key": secretRef, "base_url": configRef}}} {
				_, err = r.ApplyInvocationDefaults(input)
				require.NoError(t, err)
				value, _, err := configuration.MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
					return jobdb.ConfigurationSnapshot{Values: tc.config}, nil
				})
				require.NoError(t, err)
				refs, err := referenceDefaultRefs(value)
				require.NoError(t, err)
				session := secrets.NewSession()
				defer session.Close()
				require.NoError(t, session.PrepareReferences(context.Background(), refs, func(_ context.Context, names []string) (jobdb.SecretResolveResult, error) {
					require.Equal(t, []string{"api_key"}, names)
					return jobdb.SecretResolveResult{Secrets: tc.values}, nil
				}))
				value, err = session.Materialize(value)
				require.NoError(t, err)
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				require.JSONEq(t, tc.want, string(raw))
				err = r.ValidateMaterializedInputs(value.(map[string]any))
				if tc.invalid {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}
		})
	}
}

func TestReferenceDefaultMaterializedValidation(t *testing.T) {
	for _, tc := range []struct {
		name, expr        string
		required, invalid bool
		want              string
	}{
		{"optional omission", `${{ context.secrets.?missing.orValue(null) }}`, false, false, `{}`},
		{"required omission", `${{ context.secrets.?missing.orValue(null) }}`, true, true, `{}`},
		{"empty fallback", `${{ context.secrets.?missing.orValue("") }}`, false, true, `{"key":""}`},
		{"public fallback", `${{ context.secrets.?missing.orValue("public") }}`, true, false, `{"key":"public"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string", "x-secret": true, "minLength": 1, "default": tc.expr}}}
			if tc.required {
				schema["required"] = []any{"key"}
			}
			r, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
			require.NoError(t, err)
			input := map[string]any{}
			_, err = r.ApplyInvocationDefaults(input)
			require.NoError(t, err)
			require.NoError(t, r.ValidateInvocationInputs(input))
			session := secrets.NewSession()
			defer session.Close()
			refs, err := referenceDefaultRefs(input)
			require.NoError(t, err)
			require.NoError(t, session.PrepareReferences(context.Background(), refs, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
				return jobdb.SecretResolveResult{}, nil
			}))
			value, err := session.Materialize(input)
			require.NoError(t, err)
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(raw))
			err = r.ValidateMaterializedInputs(value.(map[string]any))
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestReferenceDefaultProviderErrors(t *testing.T) {
	r, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: referenceDefaultSchema()}})
	require.NoError(t, err)
	input := map[string]any{}
	_, err = r.ApplyInvocationDefaults(input)
	require.NoError(t, err)
	refs, err := referenceDefaultRefs(input)
	require.NoError(t, err)
	for _, providerErr := range []error{errors.New("provider outage"), jobdb.ErrNoSecretProvider, jobdb.ErrExecutionLeaseLost, context.Canceled, context.DeadlineExceeded} {
		t.Run(providerErr.Error(), func(t *testing.T) {
			session := secrets.NewSession()
			defer session.Close()
			err := session.PrepareReferences(context.Background(), refs, func(context.Context, []string) (jobdb.SecretResolveResult, error) {
				return jobdb.SecretResolveResult{}, providerErr
			})
			require.Error(t, err)
		})
	}
}

func TestInvalidReferenceDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, expr string
		secret     bool
	}{
		{"unmarked secret", `${{ context.secrets.key }}`, false},
		{"interpolated", `prefix ${{ context.secrets.key }}`, true},
		{"dynamic", `${{ context.secrets[inputs.name] }}`, true},
		{"computed fallback", `${{ context.secrets.?key.orValue(inputs.other) }}`, true},
		{"config in secret", `${{ context.config.key }}`, true},
		{"dynamic config", `${{ context.config[inputs.name] }}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prop := map[string]any{"type": "string", "default": tc.expr}
			if tc.secret {
				prop["x-secret"] = true
			}
			_, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": prop}}}})
			require.Error(t, err)
		})
	}
}

func TestConfigurationDefaultScalarTypes(t *testing.T) {
	for _, tc := range []struct{ typ, value, fallback string }{
		{"boolean", "false", "true"}, {"number", "0", "1"}, {"string", `""`, `"fallback"`}, {"null", "null", `"fallback"`},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			schema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": tc.typ, "default": "${{ context.config.?value.orValue(" + tc.fallback + ") }}"}}}
			r, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
			require.NoError(t, err)
			input := map[string]any{}
			_, err = r.ApplyInvocationDefaults(input)
			require.NoError(t, err)
			value, _, err := configuration.MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{"value": json.RawMessage(tc.value)}}, nil
			})
			require.NoError(t, err)
			require.NoError(t, r.ValidateInvocationInputs(value.(map[string]any)))
			raw, err := json.Marshal(value)
			require.NoError(t, err)
			require.JSONEq(t, `{"value":`+tc.value+`}`, string(raw))
		})
	}
}

func referenceDefaultRefs(input any) ([]secrets.Reference, error) {
	var refs []secrets.Reference
	_, err := secrets.Transform(input, func(ref secrets.Reference) (any, error) { refs = append(refs, ref); return ref, nil })
	return refs, err
}

func TestConfigurationObjectDefaultPreservesPartialOverrides(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"api": map[string]any{"type": "object", "default": "${{ context.config.api }}", "properties": map[string]any{
			"url": map[string]any{"type": "string", "default": "public"},
		}},
	}}
	r, err := RestoreResolvedOp(&ResolvedOp{Spec: ExtensionOpSpec{InputSchema: schema}})
	require.NoError(t, err)
	omitted := map[string]any{}
	_, err = r.ApplyInvocationDefaults(omitted)
	require.NoError(t, err)
	ref, ok, err := configuration.Parse(omitted["api"])
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "api", ref.Name)
	partial := map[string]any{"api": map[string]any{}}
	_, err = r.ApplyInvocationDefaults(partial)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"api": map[string]any{"url": "public"}}, partial)
	require.False(t, configuration.Contains(partial), "an explicit object must not inherit the default reference")
}
