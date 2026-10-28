package toolenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func fakeManagers(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "installs")
	t.Setenv("TOOL_INSTALL_LOG", log)
	script := `#!/bin/sh
set -eu
for spec do :; done
printf '%s\n' "$spec" >> "$TOOL_INSTALL_LOG"
[ "$spec" != broken ] || exit 4
case "${0##*/}" in
 uv) bin=$UV_TOOL_BIN_DIR;;
 pnpm) bin=$PWD/node_modules/.bin;;
esac
mkdir -p "$bin"
printf '#!/bin/sh\nprintf "%%s\\n" %s\n' "'$spec'" > "$bin/tool"
chmod +x "$bin/tool"
`
	for _, name := range []string{"uv", "pnpm"} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0700))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestVersionsScopeSelectionAndOfflineReuse(t *testing.T) {
	log := fakeManagers(t)
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "recipe", Packages: []string{"uv:tool==1"}}, {ID: "op", Packages: []string{"uv:tool==2", "pnpm:tool@3"}}}
	env, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Len(t, diag.Tools, 3)
	ctx := process.WithToolPath(context.Background(), env.Path)
	for _, tc := range []struct {
		command []string
		want    string
	}{
		{[]string{"uvx", "--from", "tool==1", "tool"}, "tool==1"},
		{[]string{"uvx", "--from", "tool==2", "tool"}, "tool==2"},
		{[]string{"pnpm", "--package=tool@3", "dlx", "tool"}, "tool@3"},
	} {
		out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: tc.command})
		require.NoError(t, err, string(stderr))
		require.Equal(t, tc.want, strings.TrimSpace(string(out)))
	}
	_, _, err = process.ExecuteProcess(ctx, process.RunRequest{Command: []string{"tool"}})
	require.Error(t, err, "same-scope executable collision must require qualification")
	_, _, err = process.ExecuteProcess(ctx, process.RunRequest{Command: []string{"uvx", "--from", "tool==9", "tool"}})
	require.Error(t, err, "undeclared versions must not install")
	before, err := os.ReadFile(log)
	require.NoError(t, err)
	again, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.Equal(t, env, again)
	for _, tool := range diag.Tools {
		require.Equal(t, "reused", tool.Outcome)
	}
	after, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	// The closest scope's unqualified executable wins without changing worker PATH.
	nearest, _, err := m.Prepare(context.Background(), []Scope{scopes[0], {ID: "op", Packages: []string{"uv:tool==2"}}})
	require.NoError(t, err)
	out, stderr, err := process.ExecuteProcess(process.WithToolPath(context.Background(), nearest.Path), process.RunRequest{Command: []string{"tool"}})
	require.NoError(t, err, string(stderr))
	require.Equal(t, "tool==2", strings.TrimSpace(string(out)))
	require.NotContains(t, os.Getenv("PATH"), nearest.Path)
}

func TestConcurrentPreparationFailureAndCacheLoss(t *testing.T) {
	log := fakeManagers(t)
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "recipe", Packages: []string{"uv:tool==1"}}}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := m.Prepare(context.Background(), scopes); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	b, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, "tool==1\n", string(b))
	env, _, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(env.Tools[0].Bin))
	require.False(t, env.Ready())
	env, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Equal(t, "prepared", diag.Tools[0].Outcome)
	_, diag, err = m.Prepare(context.Background(), []Scope{{ID: "op", Packages: []string{"pnpm:broken"}}})
	require.Error(t, err)
	require.NotEmpty(t, diag.Error)
	require.Equal(t, "failed", diag.Tools[0].Outcome)
}

func TestNixPrebuiltOnlyFailure(t *testing.T) {
	bin := t.TempDir()
	// Both metadata evaluation and realization must override ambient build
	// policy. Real binary-cache hits and qualified execution are tested in Docker.
	script := `#!/bin/sh
set -eu
[ "$1" = --extra-experimental-features ]; shift 2
[ "$1" = --max-jobs ] && [ "$2" = 0 ]; shift 2
[ "$1" = --builders ] && [ -z "$2" ]; shift 2
[ "$1" = --option ] && [ "$2" = allow-import-from-derivation ] && [ "$3" = false ]; shift 3
case "$1" in
 build)
  [ "$2" = --out-link ]
  [ "$5" = '/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-missing.drv^out' ]
  echo 'no prebuilt output available; builds disabled' >&2
  exit 1
  ;;
 eval)
  [ "$2" = --json ] && [ "$3" = --apply ] && [ "$6" = 'nixpkgs#missing' ]
  echo '{"drv":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-missing.drv","input":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-missing","bin":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-missing","outputs":{"out":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-missing"}}'
  ;;
 *) exit 2;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := Manager{Root: t.TempDir()}
	_, diag, err := m.Prepare(context.Background(), []Scope{{ID: "op", Packages: []string{"nix:nixpkgs#missing"}}})
	require.ErrorContains(t, err, "no prebuilt output available")
	require.Equal(t, "failed", diag.Tools[0].Outcome)
	manifests, err := filepath.Glob(filepath.Join(m.Root, "packages", "*", "ready.json"))
	require.NoError(t, err)
	require.Empty(t, manifests, "failed setup must not publish an installation")
}

// Opt-in smoke test against the same manager commands used in the base image.
func TestRealManagers(t *testing.T) {
	if os.Getenv("C2J_TEST_REAL_TOOLS") == "" {
		t.Skip("set C2J_TEST_REAL_TOOLS=1 for package-manager smoke test")
	}
	m := Manager{Root: t.TempDir()}
	scopes := []Scope{{ID: "op", Packages: []string{"uv:ruff==0.11.2", "pnpm:typescript@5.8.3"}}}
	env, _, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	ctx := process.WithToolPath(context.Background(), env.Path)
	for _, cmd := range [][]string{{"ruff", "--version"}, {"uvx", "--from", "ruff==0.11.2", "ruff", "--version"}, {"pnpm", "--package=typescript@5.8.3", "dlx", "tsc", "--version"}} {
		out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: cmd})
		require.NoError(t, err, string(stderr))
		require.NotEmpty(t, out)
		t.Log(strings.TrimSpace(string(out)))
	}
	_, diag, err := m.Prepare(context.Background(), scopes)
	require.NoError(t, err)
	for _, tool := range diag.Tools {
		require.Equal(t, "reused", tool.Outcome)
	}
}
