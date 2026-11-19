package recipetest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	coreops "github.com/colony-2/c2j/pkg/ops"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func scenarioSuite(t *testing.T, name string) (Case, TargetRecipe, HarnessOptions) {
	t.Helper()
	root, err := filepath.Abs("testdata/native-scenarios")
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, name+".test.json"))
	require.NoError(t, err)
	var suite struct {
		Recipe string `json:"recipe"`
		Cases  []Case `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &suite))
	data, err := os.ReadFile(filepath.Join(root, suite.Recipe))
	require.NoError(t, err)
	return suite.Cases[0], TargetRecipe{Mode: "inline_recipe", Format: "yaml", Content: string(data)}, HarnessOptions{Deps: coreops.NewServiceDepsBuilder().Build(), WorkRoot: t.TempDir(), FixtureRoot: root}
}
func registerScenarioOps(t *testing.T) {
	withRegisteredCoreOps(t, append(recipeops.GetOps(), commandop.GetOp(), squashrebasemerge.GetOp(), squashrebasemerge.GetSquashOp(), squashrebasemerge.GetMergeOp())...)
}
func runScenario(t *testing.T, c Case, target TargetRecipe, opts HarnessOptions) CaseRunResult {
	t.Helper()
	r := RunCase(context.Background(), opts, "scenario", target, c, ExecutionOptions{Timeout: "90s"})
	if r.Status != "passed" {
		raw, _ := json.MarshalIndent(r, "", "  ")
		t.Log(string(raw))
	}
	return r
}
func TestScenarioGroupedAdmissionAndSuccessor(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "admission-ordering")
	r := runScenario(t, c, target, opts)
	require.Equal(t, "passed", r.Status, r.FailureReason)
	s := r.Runtime.Controls.Scenario
	require.True(t, s.Complete)
	require.Equal(t, s.Jobs["receiver"].Ref, s.Jobs["same_receiver"].Ref)
	require.NotEqual(t, s.Jobs["receiver"].Ref.JobID, s.Jobs["successor"].Ref.JobID)
	require.Len(t, s.Receipts, 3)
	var publicationRelease int
	for _, action := range s.Actions {
		if action.ID == "finish" {
			publicationRelease = action.Position
		}
	}
	require.Positive(t, publicationRelease)
	for _, a := range s.Admission {
		if a.Kind == "admitted" && a.JobID == s.Jobs["successor"].Ref.JobID {
			require.Greater(t, a.Position, publicationRelease)
		}
	}
	for _, a := range s.Admission {
		if a.Hold == "batch" {
			require.Equal(t, "held", a.Kind)
		}
	}
	counts := map[string]string{}
	for _, call := range r.Runtime.Calls {
		if call.NodePath == "receiver/land" {
			counts[call.JobID] = call.Outputs["stdout"].(string)
		}
	}
	require.Equal(t, "2", counts[s.Jobs["receiver"].Ref.JobID])
	require.Equal(t, "1", counts[s.Jobs["successor"].Ref.JobID])
}

func TestScenarioAdvancement(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "advancement")
	r := runScenario(t, c, target, opts)
	require.Equal(t, "passed", r.Status, r.FailureReason)
}
func TestScenarioCancellation(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "cancellation")
	raw, err := os.ReadFile(filepath.Join(opts.FixtureRoot, "cancellation.test.json"))
	require.NoError(t, err)
	var suite struct {
		Cases []Case `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(raw, &suite))
	_ = c
	for _, c := range suite.Cases {
		t.Run(c.ID, func(t *testing.T) {
			r := runScenario(t, c, target, opts)
			require.Equal(t, "passed", r.Status, r.FailureReason)
		})
	}
}
func TestScenarioWorkerRecovery(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "worker-recovery")
	r := runScenario(t, c, target, opts)
	require.Equal(t, "passed", r.Status, r.FailureReason)
	s := r.Runtime.Controls.Scenario
	require.Len(t, s.Receipts, 3)
	require.Equal(t, "failed_app", s.Jobs["receiver"].State.Completion)
	require.Equal(t, s.Jobs["continuation"].Ref.JobID, s.Jobs["receiver"].Goal.SuccessfulJobID)
	require.Equal(t, s.Jobs["receiver"].Ref.JobID, s.Retained["restored-document"].ArtifactKey.JobId)
}
func TestScenarioIsolation(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "admission-ordering")
	results := make(chan CaseRunResult, 2)
	for range 2 {
		go func() {
			results <- RunCase(context.Background(), opts, "isolated-scenario", target, c, ExecutionOptions{Timeout: "90s"})
		}()
	}
	a, b := <-results, <-results
	require.Equal(t, "passed", a.Status, a.FailureReason)
	require.Equal(t, "passed", b.Status, b.FailureReason)
	require.NotEqual(t, a.Runtime.Controls.Scenario.Jobs["receiver"].Ref.JobID, b.Runtime.Controls.Scenario.Jobs["receiver"].Ref.JobID)
	require.NotEqual(t, a.Runtime.Repositories["root"].Repo, b.Runtime.Repositories["root"].Repo)
}
