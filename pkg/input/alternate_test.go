package input

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/ops"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestUnansweredPreparationAndAcceptance(t *testing.T) {
	for _, kind := range []string{"ordinary", "single", "review", "structured"} {
		t.Run(kind, func(t *testing.T) {
			cfg := reviewConfig()
			cfg.Kind = ""
			policy := UnansweredPolicy{After: "30m", Fields: map[string]any{"decision": "revise"}}
			switch kind {
			case "review":
				cfg.Kind = "review"
			case "single":
				cfg = Config{Question: "Proceed?", Type: FieldTypeBoolean}
				policy.Fields = nil
				policy.Response = false
			case "structured":
				cfg = structuredConfig()
				policy.Fields = nil
				policy.Response = map[string]any{"decision": "revise"}
			}
			deps := ops.NewOpDependenciesBuilder().Build()
			form, err := buildForm(deps, context.Background(), Input{Form: cfg, IfUnanswered: &policy})
			require.NoError(t, err)
			require.NotEmpty(t, form.RequestID)
			at, err := time.Parse(time.RFC3339Nano, form.FallbackAt)
			require.NoError(t, err)
			require.WithinDuration(t, time.Now().Add(30*time.Minute), at, time.Second)
			alt := deps.(interface {
				NextTaskAlternate() *jobworkflow.TaskAlternate
			}).NextTaskAlternate()
			require.Equal(t, alternateTaskType, alt.TaskType)
			require.Equal(t, at, alt.At)
			if policy.Fields != nil {
				policy.Fields["decision"] = "changed"
				require.Equal(t, "revise", form.Fallback.Fields["decision"])
			}
			_, err = completeUnanswered(deps, context.Background(), form)
			require.ErrorContains(t, err, "not eligible")
			form.FallbackAt = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
			out, err := completeUnanswered(deps, context.Background(), form)
			require.NoError(t, err)
			raw, err := json.Marshal(form)
			require.NoError(t, err)
			var restored InputForm
			require.NoError(t, json.Unmarshal(raw, &restored))
			replay, err := completeUnanswered(deps, context.Background(), restored)
			require.NoError(t, err)
			require.Equal(t, out["response"], replay["response"])
			require.Equal(t, out["fields"], replay["fields"])
			receipt := out["receipt"].(map[string]any)
			require.Equal(t, "if-unanswered:"+form.RequestID, receipt["submission_id"])
			require.Equal(t, "automation", receipt["actor"].(map[string]any)["kind"])
			next, err := buildForm(ops.NewOpDependenciesBuilder().Build(), context.Background(), Input{Form: cfg, IfUnanswered: &UnansweredPolicy{After: "30m", Fields: restored.Fallback.Fields, Response: restored.Fallback.Response}})
			require.NoError(t, err)
			require.NotEqual(t, form.RequestID, next.RequestID)
		})
	}
}

func TestUnansweredRejectsInvalidConfigurationBeforePublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		modify func(*Input)
	}{
		{"zero", func(in *Input) { in.IfUnanswered.After = "0s" }},
		{"negative", func(in *Input) { in.IfUnanswered.After = "-1m" }},
		{"invalid-duration", func(in *Input) { in.IfUnanswered.After = "later" }},
		{"autofill", func(in *Input) { in.Form.Output = &Output{} }},
		{"required", func(in *Input) { in.IfUnanswered.Fields = map[string]any{} }},
		{"unknown", func(in *Input) { in.IfUnanswered.Fields["unknown"] = true }},
		{"invalid-choice", func(in *Input) { in.IfUnanswered.Fields["decision"] = "unknown" }},
		{"null", func(in *Input) { in.IfUnanswered.Fields["decision"] = nil }},
		{"mixed", func(in *Input) { in.IfUnanswered.Response = "revise" }},
		{"path", func(in *Input) { in.IfUnanswered.Fields["document"] = "/tmp/doc.md" }},
		{"structured", func(in *Input) {
			in.Form = structuredConfig()
			in.IfUnanswered.Fields = nil
			in.IfUnanswered.Response = map[string]any{"decision": "invalid"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := Input{Form: reviewConfig(), IfUnanswered: &UnansweredPolicy{After: "1m", Fields: map[string]any{"decision": "revise"}}}
			test.modify(&in)
			deps := ops.NewOpDependenciesBuilder().Build()
			_, err := buildForm(deps, context.Background(), in)
			require.Error(t, err)
			require.Nil(t, deps.(interface {
				NextTaskAlternate() *jobworkflow.TaskAlternate
			}).NextTaskAlternate())
		})
	}
}
