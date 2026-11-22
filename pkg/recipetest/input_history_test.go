package recipetest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinputs"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestInputHistoryRecipeTestComposition(t *testing.T) {
	withRegisteredCoreOps(t, commandop.GetOp())
	binary := os.Getenv("C2J_TEST_INPUT_HISTORY_BINARY")
	if binary == "" {
		binary = filepath.Join(t.TempDir(), "c2j")
		b, e := exec.Command("go", "build", "-mod=readonly", "-o", binary, "../../cmd/c2j").CombinedOutput()
		require.NoError(t, e, string(b))
	}
	body := fmt.Sprintf("id: input-history-session\nsequence:\n  - id: inspect\n    op: command_execution\n    inputs:\n      run: |\n        %s job inputs list self --json\n        %s job inputs list self --json\noutputs:\n  retained: '${{ sequence.inspect.outputs.stdout }}'\n", binary, binary)
	c := Case{ID: "input-history-session", Type: "integration_case", Runtime: &RuntimeCase{}, Mocks: Mocks{Ops: []OpMock{{Match: OpMockMatch{Op: "command_execution"}, Behavior: MockBehavior{Mode: "passthrough"}}}}}
	var oldIssuer string
	for i := 0; i < 2; i++ {
		result := RunCase(context.Background(), HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir()}, "history-tests", TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: body}, c, ExecutionOptions{Timeout: "20s"})
		require.Equal(t, "passed", result.Status, result.FailureReason)
		out, ok := result.Outputs["retained"].(string)
		require.True(t, ok, "%+v", result.Outputs)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		require.Len(t, lines, 2)
		issuer := ""
		for _, line := range lines {
			r, e := jobinputs.DecodeResponse(strings.NewReader(line), "list")
			require.NoError(t, e)
			var p struct {
				Snapshot string `json:"snapshot_token"`
			}
			require.NoError(t, json.Unmarshal(r.Page, &p))
			raw, e := base64.RawURLEncoding.DecodeString(strings.Split(p.Snapshot, ".")[0])
			require.NoError(t, e)
			var token struct {
				Issuer string `json:"issuer"`
			}
			require.NoError(t, json.Unmarshal(raw, &token))
			if issuer == "" {
				issuer = token.Issuer
			} else {
				require.Equal(t, issuer, token.Issuer, "separate CLI calls used different services")
			}
		}
		require.NotEqual(t, oldIssuer, issuer, "new worker authority reused prior cache")
		oldIssuer = issuer
	}
}
