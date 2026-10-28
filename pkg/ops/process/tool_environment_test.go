package process

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeEnvironmentPrecedenceAndIsolation(t *testing.T) {
	t.Setenv("C2J_NATIVE_TEST", "ambient")
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "native-test"), []byte("#!/bin/sh\nprintf '%s|%s|%s' \"$C2J_NATIVE_TEST\" \"$PKG_CONFIG_PATH\" \"$PATH\""), 0700))
	variables := map[string]string{"PATH": "/native/bin", "C2J_NATIVE_TEST": "activation", "PKG_CONFIG_PATH": "/native/lib/pkgconfig"}
	ctx := WithToolEnvironment(context.Background(), bin, variables)
	variables["PKG_CONFIG_PATH"] = "mutated"
	out, stderr, err := ExecuteProcess(ctx, RunRequest{WorkingDir: t.TempDir(), Command: []string{"native-test"}, Env: map[string]string{"C2J_NATIVE_TEST": "op", "PATH": "/op/bin"}})
	require.NoError(t, err, string(stderr))
	require.Equal(t, "op|/native/lib/pkgconfig|"+bin+":/native/bin:/op/bin", string(out))
	require.Equal(t, "ambient", os.Getenv("C2J_NATIVE_TEST"))
	out, _, err = ExecuteProcess(context.Background(), RunRequest{Command: []string{"/bin/sh", "-c", `printf '%s' "$C2J_NATIVE_TEST"`}})
	require.NoError(t, err)
	require.Equal(t, "ambient", string(out))
}

func TestCleanPreparationEnvironment(t *testing.T) {
	t.Setenv("C2J_NATIVE_SECRET", "must-not-leak")
	ctx := WithToolEnvironment(context.Background(), "/declared/bin", map[string]string{"C2J_NATIVE_SECRET": "also-private"})
	out, stderr, err := ExecuteProcess(ctx, RunRequest{CleanEnv: true, Command: []string{"/bin/sh", "-c", `test -z "${C2J_NATIVE_SECRET+x}"; printf '%s' "$EXPLICIT"`}, Env: map[string]string{"EXPLICIT": "ok"}})
	require.NoError(t, err, string(stderr))
	require.Equal(t, "ok", string(out))
	_, _, err = ExecuteProcess(ctx, RunRequest{CleanEnv: true, Command: []string{"sh", "-c", "exit 0"}})
	require.ErrorContains(t, err, "absolute executable")
}
