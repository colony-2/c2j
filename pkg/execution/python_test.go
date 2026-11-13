package execution

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestPythonDeclarationShapeAndReplacement(t *testing.T) {
	var base Requirements
	require.NoError(t, yaml.Unmarshal([]byte("python: {requires-python: '>=3.11', dependencies: ['Pillow[extra]>=10; python_version >= \"3.11\"']}"), &base))
	var override Requirements
	require.NoError(t, yaml.Unmarshal([]byte("python: {dependencies: []}"), &override))
	got := Overlay(base, override)
	require.Empty(t, got.Python.RequiresPython)
	require.Empty(t, got.Python.Dependencies)
	require.False(t, got.Empty())
	copy := Overlay(base, Requirements{})
	copy.Python.Dependencies[0] = "changed"
	require.NotEqual(t, copy.Python, base.Python)
	rawEmpty, err := json.Marshal(Requirements{Python: &Python{}})
	require.NoError(t, err)
	require.JSONEq(t, `{"python":{"dependencies":[]},"resources":{}}`, string(rawEmpty))
	var empty Requirements
	require.NoError(t, json.Unmarshal(rawEmpty, &empty))
	require.NotNil(t, empty.Python)
	for _, value := range []Requirements{base, got} {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		var roundtrip Requirements
		require.NoError(t, json.Unmarshal(raw, &roundtrip))
		require.Equal(t, value, roundtrip)
	}
	for _, body := range []string{"[]", "null", "{requires-python: 3.13}", "{requires-python: null}", "{dependencies: null}", "{dependencies: pillow}", "{dependencies: [42]}", "{dependencies: [null]}", "{backend: uv}", "{dependencies: [], dependencies: []}"} {
		t.Run(body, func(t *testing.T) { var r Requirements; require.Error(t, yaml.Unmarshal([]byte("python: "+body), &r)) })
	}
	for _, body := range []string{`{"dependencies":[null]}`, `{"requires-python":null}`, `{"dependencies":[1]}`, `{"extra":true}`} {
		var p Python
		require.Error(t, json.Unmarshal([]byte(body), &p))
	}
	// Python grammar and conflicts are uv's responsibility.
	require.NoError(t, (&Python{RequiresPython: "not a specifier", Dependencies: []string{"invalid python requirement"}}).Validate())
}
