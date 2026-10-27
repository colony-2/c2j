package gitprofile

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestGitStderrCapturePreservesCommandOutput(t *testing.T) {
	for _, method := range []string{"run", "output", "combined"} {
		t.Run(method, func(t *testing.T) {
			redactor := secrets.NewRedactor()
			defer redactor.Close()
			ctx, log := WithStderrLog(secrets.WithRedactor(context.Background(), redactor))
			cmd := exec.CommandContext(ctx, "sh", "-c", "printf 'stdout-only'; printf 'private-' >&2; printf 'credential\\nfatal diagnostic\\n' >&2; exit 128")
			var output []byte
			var err error
			var redirected bytes.Buffer
			switch method {
			case "run":
				cmd.Stderr = &redirected
				err = Run(ctx, cmd)
				require.Contains(t, redirected.String(), "fatal diagnostic")
			case "output":
				output, err = Output(ctx, cmd)
				require.Equal(t, "stdout-only", string(output))
			case "combined":
				output, err = CombinedOutput(ctx, cmd)
				require.Contains(t, string(output), "stdout-only")
				require.Contains(t, string(output), "fatal diagnostic")
			}
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			require.Equal(t, 128, exit.ExitCode())
			if method == "output" {
				require.Contains(t, string(exit.Stderr), "fatal diagnostic")
			}
			// Publication must use the final mask set, including later registrations.
			require.NoError(t, redactor.Register("private-credential"))
			diagnostic := string(log.Bytes())
			require.Contains(t, diagnostic, "failed")
			require.Contains(t, diagnostic, "[REDACTED]\nfatal diagnostic")
			require.NotContains(t, diagnostic, "private-credential")
			require.NotContains(t, diagnostic, "stdout-only")
		})
	}
}

func TestGitStderrLogIsolationAndConcurrency(t *testing.T) {
	ctx, first := WithStderrLog(context.Background())
	_, second := WithStderrLog(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, "sh", "-c", "printf 'stdout'; printf 'warning\\n' >&2")
			_, err := CombinedOutput(ctx, cmd)
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 8, strings.Count(string(first.Bytes()), "warning"))
	require.NotContains(t, string(first.Bytes()), "stdout")
	require.Empty(t, second.Bytes())
}

func TestGitStderrLimitsOmitWholeRecords(t *testing.T) {
	ctx, log := WithStderrLog(context.Background())
	cmd := exec.CommandContext(ctx, "sh", "-c", "head -c 1048576 /dev/zero >&2; printf 'private-credential' >&2")
	require.NoError(t, Run(ctx, cmd))
	require.Contains(t, string(log.Bytes()), stderrOmitted)
	require.NotContains(t, string(log.Bytes()), "private")
	for i := 0; i < 3; i++ {
		var stderr stderrBuffer
		_, err := stderr.Write(bytes.Repeat([]byte("x"), stderrLimit/2))
		require.NoError(t, err)
		log.record(cmd, &stderr, nil)
	}
	require.LessOrEqual(t, len(log.Bytes()), stderrLimit+len(stderrOmitted))
}
