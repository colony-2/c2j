package runjob

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

func TestToolDiskRunStopsAfterHandoff(t *testing.T) {
	for _, mode := range []string{"supplied lease", "ordinary wait", "unsupported backend"} {
		t.Run(mode, func(t *testing.T) {
			bin := t.TempDir()
			t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\n' > "$UV_TOOL_BIN_DIR/disk-fixture"
chmod +x "$UV_TOOL_BIN_DIR/disk-fixture"
ln -s /dev/full ready.json.tmp
`), 0700))
			rec := `id: disk
execution: {packages: [uv:disk-fixture]}
op: command_execution
inputs: {run: 'echo must-not-run; exit 1'}
`
			var options *remote.ServerOptions
			if mode != "unsupported backend" {
				options = &remote.ServerOptions{LeaseTokenSigningKey: bytes.Repeat([]byte{42}, 32), LeaseTokenAudience: "cli-disk-fixture"}
			}
			fixture := newSuppliedCLIWithServerOptions(t, rec, time.Minute, options)
			ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 10*time.Second)
			defer cancel()
			if mode == "unsupported backend" {
				err := fixture.run(t, false)
				require.ErrorIs(t, err, jobdb.ErrResourceBackoffUnsupported)
				require.True(t, jobdb.IsExecutionInfrastructureError(err))
				info, err := fixture.backend.GetJob(ctx, fixture.key)
				require.NoError(t, err)
				require.Empty(t, info.CompletionStatus)
				return
			}
			if mode == "ordinary wait" {
				capability, err := remote.DecodeLeaseCapability(fixture.encoded)
				require.NoError(t, err)
				lease, err := fixture.client.ImportLease(ctx, capability)
				require.NoError(t, err)
				require.NoError(t, lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: lease.Route()}))
				fixture.denyClaims.Store(false)
				fixture.opts.OnNotReady = "wait"
				require.NoError(t, Run(ctx, fixture.opts), fixture.stderr.String())
			} else {
				require.NoError(t, fixture.run(t, false), fixture.stderr.String())
			}
			require.Contains(t, fixture.stdout.String(), `"kind":"job_suspended"`)
			info, err := fixture.backend.GetJob(ctx, fixture.key)
			require.NoError(t, err)
			require.NotNil(t, info.ExecutionState.ResourceResume)
			require.Empty(t, info.CompletionStatus)
			require.NotEqual(t, jobdb.JobStatusActive, info.Status, "runner must not claim a second launch")
		})
	}
}
