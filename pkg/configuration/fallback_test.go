package configuration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestFallbackRoundTripAndResolution(t *testing.T) {
	for _, tc := range []struct{ literal, raw string }{
		{`"stable"`, `"stable"`}, {`'it\'s stable'`, `"it's stable"`}, {`""`, `""`},
		{`false`, `false`}, {`true`, `true`}, {`0`, `0`}, {`-12`, `-12`}, {`1.5`, `1.5`}, {`null`, `null`},
	} {
		t.Run(tc.literal, func(t *testing.T) {
			ref, ok := Template(`${{ context.config.?setting.orValue(` + tc.literal + `) }}`)
			require.True(t, ok)
			require.JSONEq(t, tc.raw, string(ref.Default))
			for _, yamlEncoding := range []bool{false, true} {
				var value any
				if yamlEncoding {
					raw, err := yaml.Marshal(ref)
					require.NoError(t, err)
					require.NoError(t, yaml.Unmarshal(raw, &value))
				} else {
					raw, err := json.Marshal(ref)
					require.NoError(t, err)
					require.NoError(t, json.Unmarshal(raw, &value))
				}
				got, ok, err := Parse(value)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, ref, got)
				out, diag, err := MaterializeInput(context.Background(), nil, value, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
					return jobdb.ConfigurationSnapshot{Revision: "rev"}, nil
				})
				require.NoError(t, err)
				encoded, err := json.Marshal(out)
				require.NoError(t, err)
				require.JSONEq(t, tc.raw, string(encoded))
				require.Equal(t, []string{"setting"}, diag.Missing)
				require.Empty(t, diag.Values)
			}
		})
	}
	ref, ok := Expression(`context.config[?"build.channel"].orValue("stable")`)
	require.True(t, ok)
	require.Equal(t, "build.channel", ref.Name)
}

func TestFallbackOnlyAppliesToAbsentName(t *testing.T) {
	ref, ok := Expression(`context.config.?setting.orValue("fallback")`)
	require.True(t, ok)
	for _, raw := range []string{`null`, `false`, `0`, `""`, `"configured"`} {
		t.Run(raw, func(t *testing.T) {
			out, diag, err := MaterializeInput(context.Background(), nil, ref, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{"setting": json.RawMessage(raw)}}, nil
			})
			require.NoError(t, err)
			encoded, err := json.Marshal(out)
			require.NoError(t, err)
			require.JSONEq(t, raw, string(encoded))
			require.Empty(t, diag.Missing)
			require.JSONEq(t, raw, string(diag.Values["setting"]))
		})
	}
	failure := errors.New("provider unavailable")
	_, _, err := MaterializeInput(context.Background(), nil, ref, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{}, failure
	})
	require.ErrorIs(t, err, failure)
}

func TestMissingReferencesKeepPerInputDefaultsAndNullFields(t *testing.T) {
	a, _ := Expression(`context.config.?setting.orValue("a")`)
	b, _ := Expression(`context.config.?setting.orValue("b")`)
	input := map[string]any{"a": a, "b": b, "null": Ref("setting"), "list": []any{Ref("setting")}}
	calls := 0
	out, diag, err := MaterializeInput(context.Background(), nil, input, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		calls++
		return jobdb.ConfigurationSnapshot{}, nil
	})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"a": "a", "b": "b", "null": nil, "list": []any{nil}}, out)
	require.Equal(t, 1, calls)
	require.Equal(t, []string{"setting"}, diag.Missing)
	require.Empty(t, diag.Values)
	require.Equal(t, a, input["a"])
}

func TestFallbackValidation(t *testing.T) {
	for _, literal := range []string{`inputs.default`, `context.secrets.token`, `1+2`, `[]`, `{}`, `b"bytes"`} {
		_, ok := Expression(`context.config.?setting.orValue(` + literal + `)`)
		require.False(t, ok, literal)
	}
	for _, raw := range []string{`[]`, `{}`, `{"$c2j_secret":"token"}`, `invalid`} {
		ref := Ref("setting")
		ref.Default = json.RawMessage(raw)
		_, _, err := Parse(ref)
		require.Error(t, err)
	}
	for _, expr := range []string{`context.config.setting`, `context.config.?setting.orValue(null)`, `context.config.?setting.orValue(false)`} {
		ref, ok := Expression(expr)
		require.True(t, ok)
		ref.ExpectedType = "string"
		_, _, err := MaterializeInput(context.Background(), nil, ref, func(context.Context) (jobdb.ConfigurationSnapshot, error) {
			return jobdb.ConfigurationSnapshot{}, nil
		})
		require.ErrorContains(t, err, `must be string`)
	}
}
