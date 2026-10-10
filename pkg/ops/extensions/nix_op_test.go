package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/stretchr/testify/require"
)

func TestNixOpDescriptionDoesNotInstall(t *testing.T) {
	system, err := toolenv.NixSystem()
	if err != nil {
		t.Skip(err)
	}
	bin := t.TempDir()
	// Only metadata evaluation is permitted. Any attempt to install is an error.
	require.NoError(t, os.WriteFile(filepath.Join(bin, "nix"), []byte(`#!/bin/sh
set -eu
[ "$1" = --extra-experimental-features ]; shift 2
[ "$1" = --max-jobs ] && [ "$2" = 0 ]; shift 2
[ "$1" = --builders ] && [ -z "$2" ]; shift 2
[ "$1" = eval ] && [ "$2" = --json ]; shift 2
[ "$1" = --option ] && [ "$2" = allow-import-from-derivation ] && [ "$3" = false ]
printf '%s' "$NIX_TEST_DESCRIPTION"
`), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	manifest := map[string]any{
		"name": "echo", "command": []string{"bin/echo"},
		"input_schema":  map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string", "default": "hello"}}},
		"output_schema": map[string]any{"type": "object"},
	}
	for _, tc := range []struct {
		name, wantErr string
		mutate        func(map[string]any)
	}{
		{"valid", "", func(m map[string]any) {}},
		{"nix dependency", "declare Nix dependencies", func(m map[string]any) { m["dependencies"] = []string{"nix:nixpkgs#jq"} }},
		{"escape", "under bin/", func(m map[string]any) { m["command"] = []string{"bin/../../evil"} }},
		{"missing schema", "requires input_schema", func(m map[string]any) { delete(m, "input_schema") }},
		{"shell", "run and shell", func(m map[string]any) { m["run"] = "echo hello" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{}
			for k, v := range manifest {
				m[k] = v
			}
			tc.mutate(m)
			b, err := json.Marshal(map[string]any{"system": system, "store_path": "/nix/store/00000000000000000000000000000000-echo", "manifest": m})
			require.NoError(t, err)
			t.Setenv("NIX_TEST_DESCRIPTION", string(b))
			r, err := Resolve(context.Background(), "nix:github:example/ops/revision#echo", ResolveOptions{})
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.False(t, r.Ready())
			input := map[string]any{}
			_, err = r.ApplyInvocationDefaults(input)
			require.NoError(t, err)
			require.Equal(t, "hello", input["message"])
			require.NoError(t, r.ValidateInvocationInputs(input))
			// Persist/reload as job history does, with no manifest/source access.
			b, err = json.Marshal(r)
			require.NoError(t, err)
			var restored ResolvedOp
			require.NoError(t, json.Unmarshal(b, &restored))
			_, err = RestoreResolvedOp(&restored)
			require.NoError(t, err)
		})
	}
}
