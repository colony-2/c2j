package recipetest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestScenarioStandaloneSquashHistory(t *testing.T) {
	registerScenarioOps(t)
	coreops.Replace(append(coreops.List(), thinpackrebase.GetRebaseOp())...)
	_, target, opts := scenarioSuite(t, "standalone-squash-history")
	// Real file:// transport prevents Git's local clone optimization from ignoring
	// depth. The template is copied only into case-local fixture repositories.
	t.Setenv("GIT_TEMPLATE_DIR", filepath.Join(opts.FixtureRoot, "standalone-squash-template"))
	data, err := os.ReadFile(filepath.Join(opts.FixtureRoot, "standalone-squash-history.test.json"))
	require.NoError(t, err)
	var suite struct {
		Cases []Case `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &suite))
	for _, c := range suite.Cases {
		t.Run(c.ID, func(t *testing.T) {
			r := RunCase(context.Background(), opts, "scenario", target, c, ExecutionOptions{Timeout: "3m"})
			if r.Status != "passed" {
				raw, _ := json.MarshalIndent(r, "", "  ")
				t.Log(string(raw))
			}
			require.Equal(t, "passed", r.Status, r.FailureReason)
		})
	}
}
