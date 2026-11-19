package recipetest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestScenarioStrictEnvelope(t *testing.T) {
	for _, raw := range []string{`{"scenario":{"jobs":{"root":{"root":true},"root":{"root":true}}}}`, `{"scenario":{"limits":{"events":1,"events":2}}}`, `{"scenario":null}`, `{"scenario":{"jobs":null}}`, `{"scenario":{"jobs":{"root":{"root":true,"typo":true}}}}`, `{"scenario":{"limits":{"events":null}}}`, `{"scenario":{"actions":[{"id":"x","after_al":[]}]}}`} {
		t.Run(raw, func(t *testing.T) { var c RuntimeControls; require.Error(t, json.Unmarshal([]byte(raw), &c)) })
	}
}
func TestScenarioValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Scenario, *RuntimeCase)
		want string
	}{
		{"memory", func(_ *Scenario, r *RuntimeCase) { r.Backend = "memory" }, "sqlite"},
		{"missing source", func(s *Scenario, _ *RuntimeCase) { s.Jobs["one"] = ScenarioJob{} }, "one source"},
		{"mixed source", func(s *Scenario, _ *RuntimeCase) { s.Jobs["one"] = ScenarioJob{Root: true, ChildBinding: "one"} }, "one source"},
		{"unknown caller", func(s *Scenario, _ *RuntimeCase) { s.Jobs["receiver"].Submission.Caller = "absent" }, "unknown job"},
		{"missing outcome", func(s *Scenario, _ *RuntimeCase) { delete(s.Outcomes, "root") }, "missing outcome"},
		{"bad outcome", func(s *Scenario, _ *RuntimeCase) { s.Outcomes["root"] = ScenarioOutcome{Status: "eventually"} }, "invalid outcome"},
		{"unknown target", func(s *Scenario, _ *RuntimeCase) { s.Actions[0].Target = "absent" }, "unknown hold"},
		{"unknown dependency", func(s *Scenario, _ *RuntimeCase) { s.Actions[0].AfterAll = []string{"absent"} }, "unknown prerequisite"},
		{"action cycle", func(s *Scenario, _ *RuntimeCase) { s.Actions[0].AfterAll = []string{"admit"} }, "cycle"},
		{"wildcard", func(s *Scenario, _ *RuntimeCase) { s.DispatchHolds[0].Task.NodePath = "*" }, "exact"},
		{"zero attempt", func(s *Scenario, _ *RuntimeCase) { s.DispatchHolds[0].Task.Attempt = 0 }, "positive attempt"},
		{"bad disposition", func(s *Scenario, _ *RuntimeCase) { s.DispatchHolds[0].Disposition = "worker_cancelled" }, "disposition"},
		{"unseeded", func(s *Scenario, _ *RuntimeCase) { s.AdmissionHolds[0].Cell = "absent" }, "unseeded"},
		{"expected error", func(_ *Scenario, r *RuntimeCase) { r.ExpectError = "failure" }, "expect_error"},
		{"job limit", func(s *Scenario, _ *RuntimeCase) { s.Limits.Jobs = 1 }, "limit"},
		{"event limit", func(s *Scenario, _ *RuntimeCase) { s.Limits.Events = 1 }, "limit"},
		{"action limit", func(s *Scenario, _ *RuntimeCase) { s.Limits.Actions = 1 }, "limit"},
		{"receipt maximum", func(s *Scenario, _ *RuntimeCase) { s.Limits.Receipts = 4097 }, "bounds"},
		{"byte maximum", func(s *Scenario, _ *RuntimeCase) { s.Limits.ReportBytes = 8*1024*1024 + 1 }, "bounds"},
		{"unknown retained compare", func(s *Scenario, _ *RuntimeCase) {
			s.Retain = []ScenarioRetention{{ID: "proof", Job: "receiver", Task: s.DispatchHolds[0].Task, CompareTo: "absent"}}
		}, "comparison"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := scenarioSuite(t, "admission-ordering")
			tc.edit(c.Runtime.Controls.Scenario, c.Runtime)
			err := validateControls(c.Runtime)
			require.ErrorContains(t, err, tc.want)
		})
	}
}
func TestScenarioMissingTargetAndTimeout(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "admission-ordering")
	j := c.Runtime.Controls.Scenario.Jobs["receiver"]
	j.Submission.Task.NodePath = "caller/missing"
	c.Runtime.Controls.Scenario.Jobs["receiver"] = j
	r := RunCase(context.Background(), opts, "missing-target", target, c, ExecutionOptions{Timeout: "2s"})
	require.NotEqual(t, "passed", r.Status)
	require.Contains(t, r.FailureReason, "unresolved")
}
func TestScenarioForbiddenDispatch(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "admission-ordering")
	c.Runtime.Controls.Dispatch = &DispatchPolicy{Forbid: []DispatchMatcher{{Kind: "task", Name: "command_execution:command_execution", NodePath: "receiver/land", Job: "receiver"}}}
	r := runScenario(t, c, target, opts)
	require.Equal(t, "failed", r.Status)
	require.Contains(t, r.FailureReason, "forbidden")
	for _, call := range r.Runtime.Calls {
		require.NotEqual(t, "receiver/land", call.NodePath)
	}
}
func TestScenarioMetadataPredicates(t *testing.T) {
	for _, tc := range []struct {
		want  any
		match bool
	}{{float64(2), true}, {2, true}, {"2", false}, {json.RawMessage(`2.0`), false}} {
		v, e := matchesMetadata(json.RawMessage(`{"nested":{"number":2}}`), []jobdb.MetadataPredicate{{Path: []string{"nested", "number"}, Values: []any{tc.want}}})
		require.NoError(t, e)
		require.Equal(t, tc.match, v)
	}
	_, err := matchesMetadata(json.RawMessage(`{`), []jobdb.MetadataPredicate{{Path: []string{"x"}}})
	require.Error(t, err)
	_, err = matchesMetadata(json.RawMessage(`{}`), []jobdb.MetadataPredicate{{Path: []string{"x"}, Values: []any{nil}}})
	require.Error(t, err)
}
func TestScenarioPointer(t *testing.T) {
	v, err := scenarioPointer(map[string]any{"a/b": []any{map[string]any{"~": "exact"}}}, "/a~1b/0/~0")
	require.NoError(t, err)
	require.Equal(t, "exact", v)
	for _, p := range []string{"a", "/missing", "/a~1b/9", "/a~1b/no"} {
		_, err = scenarioPointer(map[string]any{"a/b": []any{}}, p)
		require.Error(t, err)
	}
	require.False(t, exact(strings.Repeat(" ", 2)))
}
func TestScenarioRestartAndRetentionFailures(t *testing.T) {
	registerScenarioOps(t)
	for _, tc := range []struct {
		name, want string
		edit       func(*Scenario)
	}{
		{"missing scope", "matches 0", func(s *Scenario) { s.Actions[3].Restart.Path = []string{"missing"} }},
		{"missing occurrence", "unavailable", func(s *Scenario) { s.Actions[3].Restart.Occurrence = 99 }},
		{"unsafe earlier scope", "retained prefix", func(s *Scenario) { s.Actions[3].Restart.Path = []string{"$job", "sequence:receiver", "op:draft"} }},
		{"changed stable request", "reused with changed", func(s *Scenario) {
			s.Actions[5].Restart.Path = []string{"$job", "sequence:receiver", "op:land"}
			s.Actions[5].Restart.RequiredPrefix = s.Actions[5].Restart.RequiredPrefix[:1]
		}},
		{"missing artifact", "expected one artifact", func(s *Scenario) { s.Retain[0].Artifact = "absent.txt" }},
		{"wrong original bytes", "bytes mismatch", func(s *Scenario) { s.Retain[0].ExpectBase64 = "d3Jvbmc=" }},
		{"wrong byte comparison", "differs", func(s *Scenario) { s.Retain[0].CompareTo = "original-native-input" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, target, opts := scenarioSuite(t, "worker-recovery")
			tc.edit(c.Runtime.Controls.Scenario)
			r := RunCase(context.Background(), opts, "scenario-negative", target, c, ExecutionOptions{Timeout: "90s"})
			require.Equal(t, "failed", r.Status)
			require.Contains(t, r.FailureReason, tc.want)
			require.False(t, r.Runtime.Controls.Complete)
		})
	}
}
func TestScenarioNativeScopePath(t *testing.T) {
	path, err := nativeScopePath(`/scope:"$job"#1/attempt:1/scope:"sequence:a/b"#2/attempt:1/scope:"op:\"publish\""#1`)
	require.NoError(t, err)
	require.Equal(t, []string{"$job", "sequence:a/b", `op:"publish"`}, path)
	for _, id := range []string{"", "fake", "/scope:\"x\"", "/scope:\"x\"#0", "/scope:\"x\"#1/attempt:0", "/scope:\"x\"#1/task:\"y\"#1"} {
		_, err := nativeScopePath(id)
		require.Error(t, err)
	}
}
func TestScenarioLifecycleMisuse(t *testing.T) {
	registerScenarioOps(t)
	for _, op := range []string{"reopen_runtime", "start_worker"} {
		t.Run(op, func(t *testing.T) {
			c, target, opts := scenarioSuite(t, "admission-ordering")
			a := &c.Runtime.Controls.Scenario.Actions[0]
			a.Do = op
			a.Target = ""
			r := RunCase(context.Background(), opts, "lifecycle-negative", target, c, ExecutionOptions{Timeout: "90s"})
			require.Equal(t, "failed", r.Status)
			require.Contains(t, r.FailureReason, "running")
		})
	}
}
func TestScenarioEvidenceBounds(t *testing.T) {
	c, _, _ := scenarioSuite(t, "admission-ordering")
	s := c.Runtime.Controls.Scenario
	s.Limits.Events = 1
	s.Limits.ReportBytes = 1
	f := &runtimeFixture{c: c}
	f.initControls()
	require.NoError(t, f.scenarioFact(ScenarioFact{ID: "first"}))
	require.ErrorContains(t, f.scenarioFact(ScenarioFact{ID: "overflow"}), "limit")
	require.Len(t, f.scenario.report.Events, 1)
	f.admissionRecord(AdmissionReceipt{JobID: "one", Kind: "held"})
	f.admissionRecord(AdmissionReceipt{JobID: "two", Kind: "held"})
	require.Len(t, f.scenario.report.Admission, 1)
	require.Contains(t, f.report.Controls.Errors, "admission evidence limit exceeded")
	require.ErrorContains(t, f.checkScenarioSize(), "byte limit")
}

func TestScenarioAdvancementFailureKeepsPublishClosed(t *testing.T) {
	registerScenarioOps(t)
	c, target, opts := scenarioSuite(t, "advancement")
	for i := range c.Mocks.Ops {
		if c.Mocks.Ops[i].Match.NodePath == "advance/edit" {
			c.Mocks.Ops[i].Behavior = MockBehavior{Mode: "fail", Error: &TestError{Message: "selected advancement failure"}}
		}
	}
	r := runScenario(t, c, target, opts)
	require.Equal(t, "failed", r.Status)
	require.Contains(t, r.FailureReason, "selected advancement failure")
	require.False(t, r.Runtime.Controls.Complete)
	for _, d := range r.Runtime.Controls.Dispatch {
		require.False(t, d.NodePath == "receiver/publish" && d.Name == "command_execution:command_execution", "publication task must remain unscheduled")
	}
}
