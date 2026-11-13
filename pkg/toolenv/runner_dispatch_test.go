package toolenv

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/pathutil"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/shellcmd"
	"github.com/stretchr/testify/require"
)

// Seed completed manifests to test dispatch through Prepare independently of
// downloads. A Nix manager declaration carries retained-root metadata too.
func seedRunnerTool(t *testing.T, m *Manager, ref, sentinel string, names ...string) {
	t.Helper()
	manager, _, err := execution.ParsePackage(ref)
	require.NoError(t, err)
	version := "v1"
	if manager == "nix" {
		version = "nix-v2"
	}
	id := key([]string{version, runtime.GOOS, runtime.GOARCH, ref})
	dir := filepath.Join(m.Root, "packages", id)
	bin := filepath.Join(dir, "bin with 'quotes'")
	require.NoError(t, os.MkdirAll(bin, 0700))
	for _, name := range names {
		// Log every call and emit an unambiguous argument stream.
		body := "#!/bin/sh\nprintf '%s\\n' " + shellcmd.QuotePOSIX(sentinel+":"+name) + " >> \"$RUNNER_CALL_LOG\"\n" +
			"if [ \"$1\" = --version ]; then " + shellcmd.LiteralLine(sentinel) + "; else printf '%s\\000' \"$@\"; fi\n"
		require.NoError(t, writeExecutable(filepath.Join(bin, name), body))
	}
	tool := Tool{Reference: ref, Identity: id, Bin: bin, Executables: names, Main: names[0]}
	if strings.Contains(ref, "no-main") {
		tool.Main = ""
	}
	if manager == "nix" {
		root := filepath.Join(dir, "root")
		require.NoError(t, os.Symlink(bin, root))
		identity, err := pathutil.CanonicalExisting(bin)
		require.NoError(t, err)
		tool.Nix = &nixTool{Input: identity, Roots: []retainedRoot{{Link: root, Target: identity}}}
	}
	require.True(t, toolReady(tool), "seeded fixture must be ready before publishing")
	b, err := json.Marshal(tool)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ready.json"), b, 0600))
}

func runnerCommand(t *testing.T, env Environment, command ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(env.Bind(context.Background()), 3*time.Second)
	defer cancel()
	out, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{Command: command})
	return string(out), string(stderr), err
}

func TestDeclaredRunnerDispatch(t *testing.T) {
	for _, layout := range []string{"direct", "symlinked"} {
		t.Run(layout, func(t *testing.T) {
			parent := t.TempDir()
			if layout == "symlinked" {
				alias := filepath.Join(t.TempDir(), "cache-parent-alias")
				require.NoError(t, os.Symlink(parent, alias))
				parent = alias
				identity, err := pathutil.CanonicalExisting(parent)
				require.NoError(t, err)
				require.NotEqual(t, identity, parent, "the cache must keep its explicit alias")
			}
			testDeclaredRunnerDispatch(t, parent)
		})
	}
}

func testDeclaredRunnerDispatch(t *testing.T, cacheParent string) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("RUNNER_CALL_LOG", log)
	require.NoError(t, os.WriteFile(log, nil, 0600))
	ambient := t.TempDir()
	for _, name := range []string{"pnpm", "nix", "uvx"} {
		require.NoError(t, writeExecutable(filepath.Join(ambient, name), "#!/bin/sh\n"+shellcmd.LiteralLine("ambient")+" >> \"$RUNNER_CALL_LOG\"\n"+shellcmd.LiteralLine("12.18.0")+"\n"))
	}
	t.Setenv("PATH", ambient+string(os.PathListSeparator)+os.Getenv("PATH"))
	originalPath := os.Getenv("PATH")
	m := &Manager{Root: filepath.Join(cacheParent, "cache with 'quotes'")}
	refs := []string{"nix:fixture#manager", "uv:qualified==1", "pnpm:qualified@1", "nix:fixture#no-main"}
	seedRunnerTool(t, m, refs[0], "12.9.0", "pnpm", "nix", "uvx")
	seedRunnerTool(t, m, refs[1], "uv-retained", "probe")
	seedRunnerTool(t, m, refs[2], "pnpm-retained", "probe")
	seedRunnerTool(t, m, refs[3], "unused", "one", "two")
	seedRunnerTool(t, m, "uv:other==2", "inner", "pnpm", "nix", "uvx")

	prepare := func(scopes []Scope) Environment {
		t.Helper()
		env, diag, err := m.Prepare(context.Background(), scopes)
		require.NoError(t, err)
		require.True(t, env.Ready())
		for _, d := range diag.Tools {
			require.Equal(t, "reused", d.Outcome)
		}
		require.Equal(t, originalPath, os.Getenv("PATH"))
		return env
	}
	selected := []Scope{{ID: "outer", Packages: refs}}
	ambiguous := []Scope{{ID: "outer", Packages: append(append([]string{}, refs...), "uv:other==2")}}
	args := []string{"", "two words", "'single'", `"double"`, "$HOME", "`echo injected`", "*", "semi;colon"}
	checkSelected := func(env Environment, want string) {
		t.Helper()
		for _, name := range []string{"pnpm", "nix", "uvx"} {
			out, stderr, err := runnerCommand(t, env, name, "--version")
			require.NoError(t, err, stderr)
			require.Equal(t, want+"\n", out)
			out, stderr, err = runnerCommand(t, env, append([]string{name}, args...)...)
			require.NoError(t, err, stderr)
			require.Equal(t, strings.Join(args, "\x00")+"\x00", out)
		}
	}
	checkQualified := func(env Environment) {
		t.Helper()
		for _, tc := range []struct {
			command []string
			want    string
		}{
			{[]string{"pnpm", "--package=qualified@1", "dlx", "probe", "--version"}, "pnpm-retained"},
			{[]string{"pnpm", "--package", "qualified@1", "dlx", "probe", "--version"}, "pnpm-retained"},
			{[]string{"uvx", "--from", "qualified==1", "probe", "--version"}, "uv-retained"},
			{[]string{"nix", "run", "fixture#manager", "--", "--version"}, "12.9.0"},
			{[]string{"nix", "run", "fixture#manager", "--version"}, "12.9.0"},
		} {
			out, stderr, err := runnerCommand(t, env, tc.command...)
			require.NoError(t, err, stderr)
			require.Equal(t, tc.want+"\n", out)
		}
		for _, prefix := range [][]string{
			{"pnpm", "--package=qualified@1", "dlx", "probe"},
			{"uvx", "--from", "qualified==1", "probe"},
			{"nix", "run", "fixture#manager", "--"},
		} {
			out, stderr, err := runnerCommand(t, env, append(prefix, args...)...)
			require.NoError(t, err, stderr)
			require.Equal(t, strings.Join(args, "\x00")+"\x00", out)
		}
	}
	env := prepare(selected)
	checkSelected(env, "12.9.0")
	checkQualified(env)
	// Duplicate declarations do not create ambiguity.
	checkSelected(prepare([]Scope{{ID: "same", Packages: []string{refs[0], refs[0]}}}), "12.9.0")
	collision := prepare(ambiguous)
	before, err := os.ReadFile(log)
	require.NoError(t, err)
	for _, name := range []string{"pnpm", "nix", "uvx"} {
		out, stderr, err := runnerCommand(t, collision, name, "--version")
		require.Error(t, err)
		require.Contains(t, err.Error(), "127")
		require.Empty(t, out)
		require.Equal(t, "ambiguous declared tool; use a qualified runner\n", stderr)
	}
	after, err := os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	checkQualified(collision)
	// Closer selection overrides both a winner and an ambiguity.
	for _, outer := range [][]Scope{selected, ambiguous} {
		checkSelected(prepare(append(append([]Scope{}, outer...), Scope{ID: "inner", Packages: []string{"uv:other==2"}})), "inner")
	}
	// Closer ambiguity overrides an outer winner.
	innerAmbiguous := prepare(append(append([]Scope{}, selected...), Scope{ID: "inner", Packages: []string{refs[0], "uv:other==2"}}))
	for _, name := range []string{"pnpm", "nix", "uvx"} {
		_, stderr, err := runnerCommand(t, innerAmbiguous, name, "--version")
		require.Error(t, err)
		require.Contains(t, stderr, "ambiguous")
	}
	before, err = os.ReadFile(log)
	require.NoError(t, err)
	for _, tc := range []struct {
		command []string
		message string
	}{
		{[]string{"pnpm", "--package=unknown", "dlx", "probe"}, "tool version not declared\n"},
		{[]string{"pnpm", "--package=qualified@1", "dlx", "unknown"}, "executable not declared by package\n"},
		{[]string{"uvx", "--from", "unknown", "probe"}, "tool version not declared\n"},
		{[]string{"uvx", "--from", "qualified==1", "unknown"}, "executable not declared by package\n"},
		{[]string{"nix", "run", "unknown"}, "tool version not declared\n"},
		{[]string{"nix", "run", "fixture#no-main"}, "package has no unambiguous main program; use its executable name\n"},
		{[]string{"pnpm", "--package"}, ""},
		{[]string{"pnpm", "--package=qualified@1", "add", "probe"}, ""},
		{[]string{"pnpm", "--package=qualified@1", "dlx"}, ""},
		{[]string{"uvx", "--from"}, ""},
		{[]string{"uvx", "--from", "qualified==1"}, ""},
		{[]string{"nix", "run"}, ""},
	} {
		out, stderr, err := runnerCommand(t, env, tc.command...)
		require.Error(t, err, "%v", tc.command)
		require.Contains(t, err.Error(), "127")
		require.Empty(t, out)
		require.Equal(t, tc.message, stderr)
	}
	after, err = os.ReadFile(log)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "negative forms must not invoke any manager or target")
	// Repair old generated wrappers at exactly the retained path.
	for _, name := range []string{"pnpm", "nix", "uvx"} {
		require.NoError(t, writeExecutable(filepath.Join(env.Path, name), "#!/bin/sh\nexec "+shellcmd.QuotePOSIX(filepath.Join(ambient, name))+" \"$@\"\n"))
	}
	again := prepare(selected)
	require.Equal(t, env.Path, again.Path)
	checkSelected(again, "12.9.0")
	checkQualified(again)
	require.Equal(t, originalPath, os.Getenv("PATH"))
	calls, err := os.ReadFile(log)
	require.NoError(t, err)
	require.NotContains(t, string(calls), "ambient", "retained packages never invoke an ambient manager, including installation")
}

func TestUndeclaredRunnerFallback(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("RUNNER_CALL_LOG", log)
	require.NoError(t, os.WriteFile(log, nil, 0600))
	ambient := t.TempDir()
	for _, name := range []string{"pnpm", "nix", "uvx"} {
		require.NoError(t, writeExecutable(filepath.Join(ambient, name), "#!/bin/sh\nprintf '%s\\000' \"$@\"\n"+shellcmd.LiteralLine("called")+" >> \"$RUNNER_CALL_LOG\"\n"))
	}
	// No host fallback can accidentally satisfy the missing-manager case.
	for _, path := range []string{ambient, t.TempDir()} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("PATH", path)
			env, _, err := (&Manager{Root: t.TempDir()}).Prepare(context.Background(), nil)
			require.NoError(t, err)
			for _, name := range []string{"pnpm", "nix", "uvx"} {
				before, err := os.ReadFile(log)
				require.NoError(t, err)
				out, stderr, err := runnerCommand(t, env, name, "--version", "", "two words")
				if path == ambient && name != "uvx" {
					require.NoError(t, err, stderr)
					require.Equal(t, "--version\x00\x00two words\x00", out)
				} else {
					require.Error(t, err)
					require.Contains(t, err.Error(), "127")
					require.Empty(t, out)
					usage := map[string]string{
						"pnpm": "use pnpm --package=PACKAGE dlx EXECUTABLE\n",
						"nix":  "use nix run INSTALLABLE -- ARGS\n",
						"uvx":  "use uvx --from PACKAGE EXECUTABLE\n",
					}
					require.Equal(t, usage[name], stderr)
					after, err := os.ReadFile(log)
					require.NoError(t, err)
					require.Equal(t, string(before), string(after))
				}
			}
		})
	}
}
