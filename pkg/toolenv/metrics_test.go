package toolenv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/preparedtest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/metrics"
	"github.com/colony-2/c2j/pkg/metricsruntime"
	"github.com/stretchr/testify/require"
)

// Scripted package managers perform only local fixture writes. The metric
// clock advances four seconds per subprocess and one per clock read, making
// both cold setup and warm lookup durations deterministic without sleeping.
func metricSetupFixture(t *testing.T) (func() time.Time, string) {
	t.Helper()
	bin := t.TempDir()
	clock := filepath.Join(t.TempDir(), "clock")
	require.NoError(t, os.WriteFile(clock, []byte("0"), 0600))
	t.Setenv("METRIC_CLOCK", clock)
	t.Setenv("METRIC_MODE", "ok")
	t.Setenv("METRIC_MARKER", filepath.Join(t.TempDir(), "entered"))
	t.Setenv("METRIC_BIN", bin)
	script := `#!/bin/sh
set -eu
if [ "${0##*/}" = pnpm ]; then
 case "$1" in
  --version) echo 12.21.0; exit 0;;
  config) echo '{"dangerouslyAllowAllBuilds":true,"strictDepBuilds":true,"ignoreScripts":false,"optional":true}'; exit 0;;
 esac
fi
n=$(cat "$METRIC_CLOCK"); echo $((n+4)) > "$METRIC_CLOCK"
: > "$METRIC_MARKER"
case "$METRIC_MODE" in fail) echo "secret-manager-error /private/fixture https://private.invalid" >&2; exit 7;; cancel) sleep 60;; esac
case "${0##*/}" in
 uv)
  case "$1 $2" in
   'tool install') mkdir -p "$UV_TOOL_BIN_DIR"; cp "$METRIC_BIN/tool" "$UV_TOOL_BIN_DIR/tool";;
   'venv --no-config') for dest do :; done; mkdir -p "$dest/bin"; cp "$METRIC_BIN/python" "$dest/bin/python"; : > "$dest/pyvenv.cfg";;
   'pip compile') : > requirements.txt;;
   'pip sync') :;;
   'pip list') echo '[]';;
   *) exit 9;;
  esac;;
 pnpm) mkdir -p node_modules/.bin; cp "$METRIC_BIN/tool" node_modules/.bin/tool;;
 python) printf '{"base":"%s/python","version":"3.12.0","files":[]}' "$METRIC_BIN";;
 builder) printf 'PATH=/fixture/bin\000CC=fixture-cc\000';;
 nix)
  op=''; link=''
  while [ "$#" -gt 0 ]; do
   case "$1" in eval|build|derivation) op=$1;; --out-link) shift; link=$1;; esac
   shift
  done
  case "$op" in
   eval) printf '{"drv":"%s.drv","input":"%s","bin":"%s","outputs":{"out":"%s"}}' "$METRIC_STORE" "$METRIC_STORE" "$METRIC_STORE" "$METRIC_STORE";;
   build) ln -s "$METRIC_STORE" "$link";;
   derivation) printf '{"fixture":{"builder":"%s/builder","env":{"stdenv":"%s","METRIC_CLOCK":"%s","METRIC_MODE":"%s","METRIC_MARKER":"%s","PATH":"%s"},"inputDrvs":{"%s.drv":["out"]}}}' "$METRIC_BIN" "$METRIC_STORE" "$METRIC_CLOCK" "$METRIC_MODE" "$METRIC_MARKER" "$PATH" "$METRIC_STORE";;
   *) exit 10;;
  esac;;
 *) exit 11;;
esac
`
	for _, name := range []string{"uv", "pnpm", "python", "builder", "nix"} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(script), 0700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	now := func() time.Time {
		b, err := os.ReadFile(clock)
		require.NoError(t, err)
		n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(clock, []byte(strconv.FormatInt(n+1, 10)), 0600))
		return time.Unix(n, 0)
	}
	return now, bin
}

func metricEnvironmentSpan(t *testing.T, channel metrics.Channel, environment, outcome string, seconds float64) {
	t.Helper()
	require.Equal(t, "valid", channel.Status)
	result, err := metrics.Reduce([]metrics.Record{{Task: `/scope:"$job"#1/attempt:1/task:"setup"#1`, Attempt: 1, Ordinal: 1, Channel: "runtime", ChannelReport: channel}}, metrics.Query{}, 1)
	require.NoError(t, err)
	var points []metrics.Point
	for _, p := range result.Snapshot.Series["c2j_task_preparation_component_seconds"] {
		require.Equal(t, map[string]string{"component": "environment", "environment": environment, "outcome": outcome}, p.Labels)
		points = append(points, p)
	}
	require.Len(t, points, 1)
	require.Equal(t, int64(1), points[0].Count)
	require.EqualValues(t, seconds, points[0].Sum)
	raw, err := json.Marshal(channel)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-manager-error")
	require.NotContains(t, string(raw), "METRIC_")
}

// All five actual setup functions run cold and warm paths and retain failed
// and canceled component spans. No package downloads or Nix realization occurs.
func TestEnvironmentMetricsAtActualSetupCalls(t *testing.T) {
	for _, environment := range []string{"uv", "npm", "python", "nix", "native"} {
		t.Run(environment, func(t *testing.T) {
			now, _ := metricSetupFixture(t)
			m := Manager{Root: t.TempDir()}
			// Reuse one pre-existing store output as an inert symlink target. This is
			// not evaluated, installed or executed. Only Nix-specific fixtures need it.
			store := ""
			if environment == "nix" || environment == "native" {
				if strings.HasPrefix(runtime.GOROOT(), "/nix/store/") {
					parts := strings.Split(strings.TrimPrefix(runtime.GOROOT(), "/nix/store/"), "/")
					store = "/nix/store/" + parts[0]
				} else {
					// Hosts with a conventional Go install may still have an existing store.
					entries, _ := filepath.Glob("/nix/store/*")
					for _, entry := range entries {
						if nixStorePath.MatchString(entry) {
							store = entry
							break
						}
					}
				}
				if store == "" {
					t.Skip("Nix fixture requires one existing inert store output")
				}
				t.Setenv("METRIC_STORE", store)
			}
			setup := func(ctx context.Context) error {
				switch environment {
				case "native":
					_, _, err := m.prepareNative(ctx, []Tool{{Nix: &nixTool{Input: store, Activate: true}}})
					return err
				case "python":
					_, _, err := m.Prepare(ctx, []Scope{{ID: "scope-secret", Python: &execution.Python{RequiresPython: ">=3.12"}}})
					return err
				default:
					prefix := environment
					if prefix == "npm" {
						prefix = "pnpm"
					}
					_, _, err := m.Prepare(ctx, []Scope{{ID: "scope-secret", Packages: []string{prefix + ":fixture"}}})
					return err
				}
			}
			cold := map[string]float64{"uv": 5, "npm": 5, "python": 21, "nix": 9, "native": 13}[environment]
			for _, mode := range []string{"cold", "reused", "fail", "cancel"} {
				t.Run(mode, func(t *testing.T) {
					if mode == "fail" || mode == "cancel" {
						m.Root = t.TempDir()
						t.Setenv("METRIC_MODE", mode)
					}
					parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					stopped := make(chan struct{})
					if mode == "cancel" {
						require.NoError(t, os.Remove(os.Getenv("METRIC_MARKER")))
						go func() {
							defer close(stopped)
							for {
								if _, err := os.Stat(os.Getenv("METRIC_MARKER")); err == nil {
									cancel()
									return
								}
								select {
								case <-parent.Done():
									return
								case <-time.After(time.Millisecond):
								}
							}
						}()
					} else {
						close(stopped)
					}
					ctx, timing := metricsruntime.Begin(preparedtest.WithClock(parent, now), "fixture", nil)
					err := setup(ctx)
					<-stopped
					outcome := "prepared"
					seconds := cold
					switch mode {
					case "cold":
						require.NoError(t, err)
					case "reused":
						require.NoError(t, err)
						outcome = "reused"
						seconds = 1
					case "fail":
						require.Error(t, err)
						outcome = "failed"
						seconds = 5
					case "cancel":
						require.Error(t, err)
						require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
						outcome = "canceled"
						seconds = 5
					}
					channels := timing.Finish(err)
					metricEnvironmentSpan(t, channels.Runtime, environment, outcome, seconds)
				})
			}
		})
	}
}
