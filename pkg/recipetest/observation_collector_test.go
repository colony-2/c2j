package recipetest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func collectorFixture(t *testing.T) (*observationCollector, context.Context, workflow.Observation) {
	t.Helper()
	root, err := template.NewRecipeResolutionContext(&contextual.GitCommitContext{}, nil, contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "root"}})
	require.NoError(t, err)
	scope, err := root.NewChildContext(template.ScopeSequence, recipe.NodeMetadata{ID: "nested"}, "", map[string]any{})
	require.NoError(t, err)
	path := scope.TaskExecutionContext().Invocation.NodePath
	report := &RuntimeReport{}
	c := newObservationCollector(ObservationConfig{Scopes: []ScopeObservationSelector{{NodePath: path, Outputs: []string{"value"}}}}, "unit", report, map[string]string{"token": "private-value"}, nil, 64*1024)
	t.Cleanup(c.redactor.Close)
	ctx := context.WithValue(context.Background(), observationContextKey{}, &observationIdentity{Path: "root/nested#1/callback:1", Site: "sequence:nested", Visit: 1, Attempt: 1})
	event := workflow.Observation{Kind: "scope_completed", Scope: scope, Output: template.ExecutionOutput{Outputs: map[string]any{"value": map[string]any{"private-value": "private-value", "list": []any{"private-value", false, nil, map[string]any{}}}}}}
	return c, ctx, event
}

func TestObservationSnapshotsRedactionAndLimits(t *testing.T) {
	key := jobdb.JobKey{TenantId: "test", JobId: "job"}
	t.Run("immutable redacted snapshot and assertions", func(t *testing.T) {
		c, ctx, event := collectorFixture(t)
		refValue := json.RawMessage(`"before"`)
		refs := map[string]recipeartifacts.Ref{"diagnostic": {Kind: recipeartifacts.RefKindValue, Format: "text", Value: refValue}}
		event.Output.Outputs["value"].(map[string]any)["reference"] = refs
		c.observe(ctx, key, event)
		copy(refValue, []byte(`"after!"`))
		delete(refs, "diagnostic")
		event.Output.Outputs["value"].(map[string]any)["list"].([]any)[0] = "mutated"
		raw, err := json.Marshal(c.report)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "private-value")
		require.NotContains(t, string(raw), "mutated")
		require.Contains(t, string(raw), "before")
		require.NotContains(t, string(raw), "after!")
		passed, message := evaluateAssertion(`observation_collection.complete && observations[0].outputs.value["[REDACTED]"] == "[REDACTED]" && observations[0].outputs.value.list[1] == false`, nil, nil, "passed", c.report)
		require.True(t, passed, message)
	})
	t.Run("record boundary", func(t *testing.T) {
		c, ctx, event := collectorFixture(t)
		c.config.Limits.Records = 1
		c.report.ObservationCollection.Limits = c.config.limits()
		c.observe(ctx, key, event)
		require.True(t, c.report.ObservationCollection.Complete)
		next := context.WithValue(ctx, observationContextKey{}, &observationIdentity{Path: "next", Visit: 2, Attempt: 1})
		for i := 0; i < 100; i++ {
			c.observe(next, key, event)
		}
		require.Len(t, c.report.Observations, 1)
		require.Len(t, c.seen, 1)
		require.False(t, c.report.ObservationCollection.Complete)
		require.Equal(t, 100, c.report.ObservationCollection.Dropped)
		require.Len(t, c.report.ObservationCollection.Diagnostics, 16)
		passed, _ := evaluateAssertion(`observation_collection.complete`, nil, nil, "passed", c.report)
		require.False(t, passed)
	})
	t.Run("exact byte boundary and one over", func(t *testing.T) {
		baseline, ctx, event := collectorFixture(t)
		baseline.observe(ctx, key, event)
		size := baseline.report.ObservationCollection.Bytes
		for _, delta := range []int{0, -1} {
			c, ctx, event := collectorFixture(t)
			c.config.Limits.Bytes = size + delta
			c.report.ObservationCollection.Limits = c.config.limits()
			c.observe(ctx, key, event)
			require.Equal(t, delta == 0, c.report.ObservationCollection.Complete)
			require.LessOrEqual(t, c.report.ObservationCollection.Bytes, size+delta)
		}
	})
	t.Run("event limit and encoding failure", func(t *testing.T) {
		for _, value := range []any{strings.Repeat("x", 256*1024), func() {}} {
			c, ctx, event := collectorFixture(t)
			event.Output.Outputs["value"] = value
			c.observe(ctx, key, event)
			require.Empty(t, c.report.Observations)
			require.False(t, c.report.ObservationCollection.Complete)
			require.Equal(t, 1, c.report.ObservationCollection.Dropped)
		}
	})
	t.Run("case aggregate shared by jobs", func(t *testing.T) {
		c, ctx, event := collectorFixture(t)
		c.config.Limits.Records = 1
		c.report.ObservationCollection.Limits = c.config.limits()
		c.observe(ctx, key, event)
		c.observe(ctx, jobdb.JobKey{TenantId: "test", JobId: "child"}, event)
		require.Len(t, c.report.Observations, 1)
		require.False(t, c.report.ObservationCollection.Complete)
	})
	t.Run("missing collection cannot pass", func(t *testing.T) {
		passed, _ := evaluateAssertion(`observation_collection.complete && observations.size() == 1`, nil, nil, "passed", nil)
		require.False(t, passed)
	})
}

func TestObservationValidation(t *testing.T) {
	valid := observationSelection()
	require.NoError(t, validateObservations(valid))
	for _, invalid := range []*ObservationConfig{
		{}, {Limits: ObservationLimits{Records: -1}, Scopes: valid.Scopes},
		{Limits: ObservationLimits{Records: 4097}, Scopes: valid.Scopes},
		{Limits: ObservationLimits{Bytes: 8388609}, Scopes: valid.Scopes},
		{Scopes: []ScopeObservationSelector{{NodePath: "*", Outputs: []string{"ok"}}}},
		{Scopes: []ScopeObservationSelector{{NodePath: "node"}}},
		{Scopes: []ScopeObservationSelector{{NodePath: "node", Outputs: []string{"ok"}, RetainArtifacts: []string{"../escape"}}}},
		{Transitions: []TransitionObservationSelector{{MachinePath: "node", From: "verify"}}},
		{Entries: []EntryObservationSelector{{MachinePath: "node"}}},
	} {
		require.Error(t, validateObservations(invalid))
	}
}

func TestObservationArtifactOmissions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ref     recipeartifacts.Ref
		missing bool
		limit   int
	}{
		{name: "missing", missing: true},
		{name: "external", ref: recipeartifacts.NewExternalRef("diagnostic", "https://production.invalid/no-fetch", false)},
		{name: "oversized", ref: recipeartifacts.Ref{Kind: recipeartifacts.RefKindValue, Format: "text", Value: json.RawMessage(`"too long"`)}, limit: 2},
		{name: "escaping", ref: recipeartifacts.Ref{Kind: recipeartifacts.RefKindValue, Format: "text", Value: json.RawMessage(`"x"`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ctx, event := collectorFixture(t)
			name := "diagnostic"
			if tc.name == "escaping" {
				name = "../../../escape"
			}
			c.config.Scopes[0].RetainArtifacts = []string{name}
			event.Output.ArtifactRefs = map[string]recipeartifacts.Ref{}
			if !tc.missing {
				event.Output.ArtifactRefs[name] = tc.ref
			}
			if tc.limit > 0 {
				c.artifactLimit = tc.limit
			}
			c.observe(ctx, jobdb.JobKey{TenantId: "test", JobId: "job"}, event)
			require.False(t, c.report.ObservationCollection.Complete)
			require.Empty(t, c.artifacts)
			require.Len(t, c.report.ObservationArtifacts, 1)
			require.NotEqual(t, "retained", c.report.ObservationArtifacts[0].Status)
			require.Empty(t, c.report.ObservationArtifacts[0].Path)
		})
	}
}

func TestObservationLinkAssertionsRejectMissingAndAmbiguousRecords(t *testing.T) {
	expr := `observation_collection.complete && observations.filter(o, o.kind == "state_entered").size() == 1 && observations.filter(o, o.kind == "state_entered").all(o, o.transition_id != "" && o.source_scope_id != "")`
	for _, records := range [][]map[string]any{
		{},
		{{"kind": "state_entered", "transition_id": "", "source_scope_id": ""}},
		{{"kind": "state_entered", "transition_id": "selected", "source_scope_id": "state"}, {"kind": "state_entered", "transition_id": "selected2", "source_scope_id": "state2"}},
	} {
		passed, _ := evaluateAssertion(expr, nil, nil, "passed", &RuntimeReport{ObservationCollection: &ObservationCollection{Complete: true}, Observations: records})
		require.False(t, passed)
	}
}

func TestObservationDiagnosticIdentityRedactsBeforeTruncation(t *testing.T) {
	c, _, _ := collectorFixture(t)
	c.omit(strings.Repeat("x", 1020)+"private-value", "identity_limit")
	diagnostic := c.report.ObservationCollection.Diagnostics[0]["id"].(string)
	require.NotContains(t, diagnostic, "priv")
	require.LessOrEqual(t, len(diagnostic), 1050)
}

// OBS-020/021: an occupied retrieval key must never overwrite retained bytes.
func TestObservationArtifactCollisionIsExplicit(t *testing.T) {
	c, ctx, event := collectorFixture(t)
	key := jobdb.JobKey{TenantId: "test", JobId: "job"}
	event.Output.ArtifactRefs = map[string]recipeartifacts.Ref{"diagnostic": {Kind: recipeartifacts.RefKindValue, Format: "text", Value: json.RawMessage(`"first"`)}}
	c.retain(ctx, key, "observation-1", "diagnostic", event.Output)
	require.Len(t, c.artifacts, 1)
	first := c.report.ObservationArtifacts[0]
	require.Equal(t, "retained", first.Status)
	event.Output.ArtifactRefs["diagnostic"] = recipeartifacts.Ref{Kind: recipeartifacts.RefKindValue, Format: "text", Value: json.RawMessage(`"replacement"`)}
	c.retain(ctx, key, "observation-1", "diagnostic", event.Output)
	require.Len(t, c.artifacts, 1)
	require.Equal(t, "first", string(c.artifacts[strings.TrimPrefix(first.Path, "artifacts/")]))
	require.False(t, c.report.ObservationCollection.Complete)
	require.Len(t, c.report.ObservationArtifacts, 2)
	require.Equal(t, "omitted", c.report.ObservationArtifacts[1].Status)
	require.Empty(t, c.report.ObservationArtifacts[1].Path)
	raw, err := json.Marshal(c.report)
	require.NoError(t, err)
	require.Contains(t, string(raw), "artifact_collision")
}
