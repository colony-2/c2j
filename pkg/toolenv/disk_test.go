package toolenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func TestToolDiskNixEvidence(t *testing.T) {
	exit := exec.Command("sh", "-c", "exit 1").Run()
	require.IsType(t, &exec.ExitError{}, exit)
	path := "/nix/store/tmp-175586-0/x/pkgs/misc/signed-packages/by-name/fw"
	encode := func(message string) string {
		raw, err := json.Marshal(message)
		require.NoError(t, err)
		return fmt.Sprintf(`@nix {"action":"msg","level":0,"raw_msg":%s}`, raw)
	}
	diagnostic := encode("creating directory '" + path + "': No space left on device")
	for _, test := range []struct {
		name, stderr, want string
		err                error
		confirmed          bool
	}{
		{"store path", diagnostic, path, exit, true},
		{"pathless write", encode("writing to file: No space left on device"), "", exit, true},
		{"colored byte count", encode("write of \x1b[35;1m21\x1b[0m bytes: \x1b[35;1mNo space left on device\x1b[0m"), "", exit, true},
		{"filesystem exception", `@nix {"action":"msg","level":0,"msg":"\u001b[31;1merror:\u001b[0m filesystem error: cannot create directory: No space left on device [` + path + `]"}`, path, exit, true},
		{"unrelated path", encode("creating directory '/tmp/tools': No space left on device"), "", exit, false},
		{"path out of store", encode("creating directory '/nix/store/../../tmp': No space left on device"), "", exit, false},
		{"plain stderr", "error: creating directory '" + path + "': No space left on device", "", exit, false},
		{"warning followed by error", diagnostic + "\n" + encode("download failed"), "", exit, false},
		{"unrelated text", encode("builder logged No space left on device under " + path), "", exit, false},
		{"cancelled", diagnostic, "", context.Canceled, false},
		{"uncertain cleanup", diagnostic, "", errors.Join(exit, errors.New("cleanup failed")), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, _, confirmed := nixPrefetchDiskFailure(test.err, []byte(test.stderr))
			require.Equal(t, test.confirmed, confirmed)
			require.Equal(t, test.want, path)
		})
	}
}

func TestToolDiskNixPrefetchFallback(t *testing.T) {
	if !process.CheckedTreeCleanupSupported {
		t.Skip("prefetch disk recovery requires process tree cleanup")
	}
	for _, test := range []struct {
		name, effectiveStore string
		mode                 string
		prefetch             bool
	}{
		{"unavailable config probe", "", "1", false},
		{"unverified layout", "local", "unverified", false},
		{"unconfirmed exit 1", "local://", "1", true},
		{"unconfirmed exit 2", "local", "2", true},
		{"signaled prefetch", "local", "signal", true},
		{"canceled prefetch", "local", "cancel", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bin := t.TempDir()
			marker := filepath.Join(bin, "prefetch-ran")
			evalMarker := filepath.Join(bin, "eval-ran")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("NIX_REMOTE", "local")
			t.Setenv("C2J_TEST_NIX_STORE", test.effectiveStore)
			t.Setenv("C2J_TEST_NIX_PREFETCH_MARKER", marker)
			t.Setenv("C2J_TEST_NIX_EVAL_MARKER", evalMarker)
			t.Setenv("C2J_TEST_NIX_PREFETCH_MODE", test.mode)
			system, err := NixSystem()
			require.NoError(t, err)
			t.Setenv("C2J_TEST_NIX_SYSTEM", system)
			require.NoError(t, os.WriteFile(filepath.Join(bin, "nix"), []byte(`#!/bin/sh
set -eu
case " $* " in
  *' config show store '*)
    if [ -z "$C2J_TEST_NIX_STORE" ]; then exit 1; fi
    printf '%s\n' "$C2J_TEST_NIX_STORE" ;;
  ' __dump-cli ')
    if [ "$C2J_TEST_NIX_PREFETCH_MODE" = "unverified" ]; then echo '{}'; exit 0; fi
    echo '{"stores":{"Local Store":{"settings":{"root":{"value":null},"store":{"value":"/nix/store"},"real":{"value":"/nix/store"},"state":{"value":"/nix/var/nix"},"log":{"value":"/nix/var/log/nix"}}}}}' ;;
  *' flake prefetch '*)
    touch "$C2J_TEST_NIX_PREFETCH_MARKER"
    case "$C2J_TEST_NIX_PREFETCH_MODE" in
      signal) kill -KILL "$$" ;;
      cancel) exec sleep 30 ;;
      *) echo 'error: download unavailable offline' >&2; exit "$C2J_TEST_NIX_PREFETCH_MODE" ;;
    esac ;;
  *' eval '*)
    touch "$C2J_TEST_NIX_EVAL_MARKER"
    printf '{"store_path":"/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-fixture","system":"%s","manifest":{}}\n' "$C2J_TEST_NIX_SYSTEM" ;;
  *) exit 70 ;;
esac
`), 0700))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if test.mode == "cancel" {
				go func() {
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if _, err := os.Stat(marker); err == nil {
								cancel()
								return
							}
						}
					}
				}()
			}
			pkg, err := DescribeNixPackage(ctx, "nix:github:c2j-test/offline-fixture/0000000000000000000000000000000000000001#default", t.TempDir())
			switch test.mode {
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
				require.NoFileExists(t, evalMarker)
			case "signal":
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				require.Equal(t, -1, exit.ExitCode())
				require.NoFileExists(t, evalMarker)
			default:
				require.NoError(t, err)
				require.Equal(t, "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-fixture", pkg.StorePath)
				require.FileExists(t, evalMarker)
			}
			if test.prefetch {
				require.FileExists(t, marker)
			} else {
				require.NoFileExists(t, marker)
			}
		})
	}
}

func TestToolDiskNixPrefetchUncertainCleanup(t *testing.T) {
	exit := exec.Command("sh", "-c", "exit 2").Run()
	require.IsType(t, &exec.ExitError{}, exit)
	cleanup := errors.New("process group cleanup failed")
	err := nixPrefetchResult(t.Context(), nil, errors.Join(exit, cleanup))
	require.ErrorIs(t, err, cleanup)
	_, disk := DiskExhaustion(err)
	require.False(t, disk)
}

func TestToolDiskNixLayoutWithActivation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires the Linux worker's default Nix layout")
	}
	if _, err := exec.LookPath("nix"); err != nil && os.Getenv("C2J_REQUIRE_TOOL_DISK_NIX") != "1" {
		t.Skip("requires Nix")
	}
	t.Setenv("NIX_REMOTE", "local")
	t.Setenv("NIX_CONFIG", "store = local")
	defaultDirs := map[string]string{"NIX_STORE_DIR": "/nix/store", "NIX_STATE_DIR": "/nix/var/nix", "NIX_LOG_DIR": "/nix/var/log/nix"}
	workDir := t.TempDir()
	require.True(t, defaultLocalNixLayout(process.WithToolEnvironment(t.Context(), "", defaultDirs), workDir))
	for key := range defaultDirs {
		t.Run(key, func(t *testing.T) {
			custom := make(map[string]string, len(defaultDirs))
			for key, path := range defaultDirs {
				custom[key] = path
			}
			custom[key] = t.TempDir()
			ctx := process.WithToolEnvironment(t.Context(), "", custom)
			store, err := runNix(ctx, workDir, "config", "show", "store")
			require.NoError(t, err)
			require.Equal(t, "local\n", string(store))
			require.False(t, defaultLocalNixLayout(ctx, workDir))
		})
	}
}

func TestToolDiskErrorProvenance(t *testing.T) {
	path := "/path/to/cache/ready.json.tmp"
	local := &os.PathError{Op: "write", Path: path, Err: syscall.ENOSPC}
	diagnostic := errors.New("error: writing to file: No space left on device")
	for _, test := range []struct {
		name, want string
		err        error
	}{
		{"kernel error", path, fmt.Errorf("prepare tool: %w", local)},
		{"confirmed Nix error", "/nix/store/tmp-fixture", fmt.Errorf("prepare tool: %w", &nixDiskError{error: diagnostic, path: "/nix/store/tmp-fixture"})},
		{"plain output", "", diagnostic},
		{"flattened kernel error", "", errors.New(local.Error())},
		{"ordinary error", "", &os.PathError{Op: "write", Path: path, Err: syscall.EACCES}},
		{"mixed causes", "", errors.Join(local, errors.New("cleanup failed"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, confirmed := DiskExhaustion(test.err)
			require.Equal(t, test.want != "", confirmed)
			if confirmed {
				require.Equal(t, test.want, got)
			}
		})
	}
}
