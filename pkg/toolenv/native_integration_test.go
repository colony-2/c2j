package toolenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/nixtest"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func TestRealNativeEnvironment(t *testing.T) {
	if !nixtest.InContainer(t) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Setenv("NIX_CONFIG", "max-jobs = 1\nbuilders = ssh://c2j-no-build.invalid\n")
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "repository:root", Packages: []string{
		"nix:" + nativeNixpkgs + "#openssl", "nix:" + nativeNixpkgs + "#zlib",
	}}}
	env, diag, err := m.Prepare(ctx, scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Equal(t, "prepared", diag.Native.Outcome)
	require.Empty(t, env.Tools[1].Executables, "zlib needs no bin directory")
	require.Contains(t, env.Variables["PKG_CONFIG_PATH"], "-openssl-")
	require.Contains(t, env.Variables["NIX_CFLAGS_COMPILE"], "-zlib-")
	require.NotContains(t, env.Variables, "LD_LIBRARY_PATH")
	require.NotContains(t, env.Variables, "HOME")
	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "probe.c"), []byte(`#include <openssl/evp.h>
#include <zlib.h>
#include <stdio.h>
#include <string.h>
int main(void) {
 const unsigned char msg[] = "c2j native packages";
 unsigned char buf[128], out[128], digest[EVP_MAX_MD_SIZE];
 uLongf n = sizeof(buf), m = sizeof(out); unsigned int size;
 if (compress(buf, &n, msg, sizeof(msg)) != Z_OK) return 1;
 if (uncompress(out, &m, buf, n) != Z_OK || m != sizeof(msg) || memcmp(msg, out, m)) return 2;
 if (!EVP_Digest(msg, sizeof(msg), digest, &size, EVP_sha256(), NULL) || size != 32) return 3;
 puts("native-libraries-ok"); return 0;
}`), 0600))
	run := func(e Environment) {
		t.Helper()
		out, stderr, err := process.ExecuteProcess(e.Bind(ctx), process.RunRequest{WorkingDir: work, Shell: "sh", Run: "pkg-config --modversion openssl zlib; cc probe.c -o probe $(pkg-config --cflags --libs openssl zlib); ./probe; cc probe.c -o direct -lcrypto -lz; ./direct"})
		require.NoError(t, err, string(stderr))
		require.Equal(t, 2, strings.Count(string(out), "native-libraries-ok"))
	}
	run(env)
	out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{CleanEnv: true, WorkingDir: work, Command: []string{filepath.Join(work, "probe")}})
	require.NoError(t, err, string(stderr))
	require.Contains(t, string(out), "native-libraries-ok")
	// Serialization and cache reuse must not invoke any manager or activation hook.
	t.Run("offline_warm", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		raw, err := json.Marshal(env)
		require.NoError(t, err)
		var restored Environment
		require.NoError(t, json.Unmarshal(raw, &restored))
		again, diag, err := m.Prepare(ctx, scopes)
		require.NoError(t, err)
		require.Equal(t, restored, again)
		require.Equal(t, "reused", diag.Native.Outcome)
		for _, tool := range diag.Tools {
			require.Equal(t, "reused", tool.Outcome)
		}
		run(again)
	})
	_, err = runNix(ctx, work, "store", "gc")
	require.NoError(t, err)
	require.True(t, env.Ready())
	run(env)
	// Losing a GC root makes readiness fail and repairs only future preparation.
	require.NoError(t, os.Remove(env.Native.Roots[0].Link))
	require.False(t, env.Ready())
	again, diag, err := m.Prepare(ctx, scopes)
	require.NoError(t, err)
	require.True(t, again.Ready())
	require.Equal(t, "prepared", diag.Native.Outcome)
	run(again)
	t.Run("declared_compiler", func(t *testing.T) {
		declared := []Scope{{ID: "op", Packages: append(append([]string(nil), scopes[0].Packages...), "nix:"+nativeNixpkgs+"#stdenv.cc", "nix:"+nativeNixpkgs+"#pkg-config")}}
		explicit, _, err := m.Prepare(ctx, declared)
		require.NoError(t, err)
		require.True(t, explicit.Tools[2].Nix.Compiler)
		run(explicit)
	})
	t.Logf("native activation: %s; %d package outputs", nativeNixpkgs, len(env.Tools))
}
