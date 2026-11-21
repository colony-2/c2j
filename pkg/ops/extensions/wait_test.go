package extensions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/preparedtest"
	"github.com/colony-2/c2j/pkg/extensionwait"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/stretchr/testify/require"
)

func TestExtensionWaitProcess(t *testing.T) {
	for _, tc := range []struct {
		name, response   string
		exit             int
		discard, success bool
	}{
		{"W08_bypass_schema", `{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}`, 76, true, false},
		{"W09_business_output", `{"output":{"ready":true,"wait":{"state":"business"}}}`, 0, false, true},
		{"W09_exit75_unchanged", `{"execution_required":{"base":"other*"}}`, 75, false, false},
		{"W10_failure", `{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}`, 7, false, false},
		{"W10_schema_failure", `{"output":{}}`, 0, false, false},
		{"W57_success", `{"output":{"ready":true}}`, 0, false, true},
		{"W03_mixed_metrics", `{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"},"metrics":{}}`, 76, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := invokeNegotiatingExtension(t, context.Background(), fmt.Sprintf("printf '%%s' '%s'; exit %d", tc.response, tc.exit), nil)
			require.Equal(t, tc.discard, extensionwait.IsDiscard(err))
			if tc.success {
				require.NoError(t, err)
				require.Equal(t, true, out["ready"])
			} else {
				require.Error(t, err)
				require.Nil(t, out)
			}
		})
	}
	t.Run("W11_capability", func(t *testing.T) {
		t.Setenv(extensionwait.CapabilityEnv, "forged")
		out, err := invokeNegotiatingExtension(t, context.Background(), `printf '{"output":{"ready":true,"version":"%s"}}' "$C2J_EXTENSION_WAIT_VERSION"`, map[string]string{extensionwait.CapabilityEnv: "override"})
		require.NoError(t, err)
		require.Equal(t, "1", out["version"])
	})
	t.Run("W12_secret", func(t *testing.T) {
		r := secrets.NewRedactor()
		defer r.Close()
		require.NoError(t, r.Register("private-target"))
		ctx := secrets.WithRedactor(context.Background(), r)
		s := r.NewSession()
		defer s.Close()
		ctx = secrets.WithSession(ctx, s)
		_, err := invokeNegotiatingExtension(t, ctx, `printf '{"wait":{"version":1,"dependencies":[{"job":"private-target","condition":"success"}]}}'; exit 76`, nil)
		require.Error(t, err)
		require.False(t, extensionwait.IsDiscard(err))
		require.NotContains(t, err.Error(), "private-target")
	})
	t.Run("W08_truncated", func(t *testing.T) {
		_, err := invokeNegotiatingExtension(t, context.Background(), `head -c 5000000 /dev/zero; exit 76`, nil)
		require.Error(t, err)
		require.False(t, extensionwait.IsDiscard(err))
	})
}

func TestExtensionWaitPreparedCapability(t *testing.T) {
	ctx := preparedtest.WithCompletion(context.Background(), func(_ context.Context, in preparedtest.Invocation) ([]byte, bool, error) {
		require.Equal(t, "1", in.Env[extensionwait.CapabilityEnv])
		err := exec.Command("sh", "-c", "exit 76").Run()
		return []byte(`{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}`), true, err
	})
	out, err := invokeNegotiatingExtension(t, ctx, "exit 99", map[string]string{extensionwait.CapabilityEnv: "forged"})
	require.Nil(t, out)
	require.True(t, extensionwait.IsDiscard(err))
}

func TestExtensionWaitStopsManagedChildren(t *testing.T) {
	for _, redirect := range []string{" >/dev/null 2>&1", ""} {
		t.Run(fmt.Sprintf("redirected=%t", redirect != ""), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "survived")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := invokeNegotiatingExtension(t, ctx, fmt.Sprintf(`(sleep 0.3; printf survived > %q)%s & printf '{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}'; exit 76`, marker, redirect), nil)
			require.True(t, extensionwait.IsDiscard(err))
			time.Sleep(450 * time.Millisecond)
			_, err = os.Stat(marker)
			require.True(t, os.IsNotExist(err), "managed background process must be stopped before cleanup")
		})
	}
}
