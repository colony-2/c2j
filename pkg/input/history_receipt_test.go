package input

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

type historyReceiptFixture struct {
	name, taskType, form, output, provenance, source string
}

// Use actual producer functions to keep the distinction between echoed ordinary
// metadata and generated guarded receipts tied to the writing semantics.
func historyReceiptFixtures(t *testing.T) []historyReceiptFixture {
	t.Helper()
	ctx := celltest.Context(context.Background())
	deps := coreops.NewOpDependenciesBuilder().Build()
	raw := func(v any) string { b, e := json.Marshal(v); require.NoError(t, e); return string(b) }
	caller := &Receipt{RequestID: "caller-request", SubmissionID: "caller-submission", SubmittedAt: time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC), Actor: Actor{ID: "caller-chosen-human", Kind: "human"}}
	ordinary := InputForm{Question: "q", Type: FieldTypeBoolean, Output: &Output{Response: false, Receipt: caller}}
	out, err := autoFillInputWithDeps(deps, ctx, ordinary)
	require.NoError(t, err)
	fixtures := []historyReceiptFixture{{"ordinary-reported", "auto-fill-input:echo", raw(ordinary), raw(out), "reported_receipt", "autofill"}}
	for _, kind := range []string{"review", "structured"} {
		cfg := reviewConfig()
		if kind == "structured" {
			cfg = structuredConfig()
		}
		form, e := buildForm(deps, ctx, Input{Form: cfg})
		require.NoError(t, e)
		if kind == "review" {
			form.Output = &Output{Fields: map[string]any{"decision": "approve"}}
		} else {
			form.Output = &Output{Response: map[string]any{"decision": "approve"}}
		}
		generated, e := autoFillInputWithDeps(deps, ctx, form)
		require.NoError(t, e)
		require.Equal(t, "automation", generated.Receipt.Actor.Kind)
		fixtures = append(fixtures, historyReceiptFixture{kind + "-generated", "auto-fill-input:echo", raw(form), raw(generated), "automation_receipt", "autofill"})
		if kind == "review" {
			binder := newArtifactBinder(ctx, deps, nil)
			external, e := acceptReview(form, FormSubmission{RequestID: form.RequestID, SubmissionID: "authenticated", Fields: map[string]any{"decision": "approve"}}, Actor{ID: "authenticated-human", Kind: "human"}, binder)
			binder.cleanup()
			require.NoError(t, e)
			fixtures = append(fixtures, historyReceiptFixture{"external-authenticated", "input:collect_user_input", raw(form), raw(external), "submission_receipt", "external_submission"})
		}
	}
	fallback := InputForm{RequestID: "fallback-request", Question: "q", Type: FieldTypeBoolean, Fallback: &FallbackAnswers{Response: false}}
	generated, err := acceptFallback(deps, ctx, fallback)
	require.NoError(t, err)
	fixtures = append(fixtures, historyReceiptFixture{"fallback-generated", "input-alternate:complete", raw(fallback), raw(generated), "automation_receipt", "unanswered_fallback"},
		historyReceiptFixture{"missing", "auto-fill-input:echo", `{"question":"q"}`, `{"response":false}`, "unavailable", "autofill"},
		historyReceiptFixture{"legacy", "auto-fill-input:echo", `{"question":"q"}`, `{"response":false,"user_id":"reported"}`, "reported_legacy", "autofill"})
	return fixtures
}

func assertHistoryReceipt(t *testing.T, s HistoryService, ctx context.Context, tenant, job string, fixture historyReceiptFixture) {
	t.Helper()
	page, err := s.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: tenant, JobID: job})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	entry := page.Entries[0]
	require.Equal(t, fixture.provenance, entry.Actor.Provenance)
	require.Equal(t, fixture.source, entry.ResponseSource)
	require.Equal(t, fixture.output, string(entry.AcceptedResponse.Raw))
	output, err := historyFields(json.RawMessage(fixture.output))
	require.NoError(t, err)
	if receipt, ok := output["receipt"]; ok {
		require.Equal(t, string(receipt), string(entry.Actor.Receipt))
	} else {
		require.Empty(t, entry.Actor.Receipt)
	}
	detail, err := s.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: tenant, JobID: job, EntryID: entry.EntryID, SnapshotToken: page.SnapshotToken})
	require.NoError(t, err)
	require.Equal(t, entry.Actor, detail.Actor)
	require.Equal(t, entry.AcceptedResponse.Raw, detail.AcceptedResponse.Raw)
	require.Equal(t, entry.AcceptedResponse.Raw, detail.RawAcceptedOutput)
}

func TestHistoryReceiptProducerProvenance(t *testing.T) {
	for _, fixture := range historyReceiptFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			r := historyFixture(historyTask(1, "/task:answer", fixture.taskType), historyResult(2, "/task:answer/attempt:1", fixture.form, fixture.output))
			assertHistoryReceipt(t, historyNew(t, r), context.Background(), "tenant", "job", fixture)
		})
	}
}
