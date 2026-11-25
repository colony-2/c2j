package extensions

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"github.com/colony-2/c2j/internal/extensioneagain"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

func TestExtensionEAGAINExit77RequiresCapability(t *testing.T) {
	t.Setenv(extensioneagain.CapabilityEnv, "1")
	env := map[string]string{extensioneagain.CapabilityEnv: "1"}
	script := `test "$C2J_EXTENSION_EAGAIN_VERSION" = %s || exit 42; printf 'Permission denied by extension\n' >&2; printf '%%s' '{"eagain":{"version":1,"mode":"abandon"}}'; exit 77`

	t.Run("ordinary_failure_without_capability", func(t *testing.T) {
		out, err := invokeNegotiatingExtension(t, context.Background(), fmt.Sprintf(script, "0"), env)
		require.Nil(t, out)
		require.EqualError(t, err, "Permission denied by extension [exit 77]")
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		require.Equal(t, extensioneagain.ExitCode, exit.ExitCode())
	})

	t.Run("control_with_capability", func(t *testing.T) {
		if !process.CheckedTreeCleanupSupported {
			t.Skip("EAGAIN requires checked process-tree cleanup")
		}
		ctx := extensioneagain.WithSupport(context.Background(), nil)
		out, err := invokeNegotiatingExtension(t, ctx, fmt.Sprintf(script, "1"), env)
		require.Nil(t, out)
		var control *extensioneagain.Control
		require.ErrorAs(t, err, &control)
		require.Equal(t, extensioneagain.Request{Version: 1, Mode: extensioneagain.Abandon}, control.Request)
	})
}
