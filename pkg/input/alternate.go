package input

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/google/uuid"
)

const alternateTaskType = "input-alternate:complete"

// UnansweredPolicy supplies ordinary answers if a worker acquires the alternate
// after the delay. It does not impose a deadline on human responses.
type UnansweredPolicy struct {
	After        string                   `json:"after"`
	Response     any                      `json:"response,omitempty"`
	Fields       map[string]any           `json:"fields,omitempty"`
	ArtifactRefs map[string]artifacts.Ref `json:"artifact_refs,omitempty"`
}

// FallbackAnswers is frozen in the prepared form, together with FallbackAt.
type FallbackAnswers struct {
	Response     any                      `json:"response,omitempty"`
	Fields       map[string]any           `json:"fields,omitempty"`
	ArtifactRefs map[string]artifacts.Ref `json:"artifact_refs,omitempty"`
}

func (p UnansweredPolicy) validate(form Config) error {
	duration, err := time.ParseDuration(p.After)
	if err != nil || duration <= 0 {
		return fmt.Errorf("if_unanswered.after must be a positive duration")
	}
	if form.Output != nil {
		return fmt.Errorf("if_unanswered and form.autofill are mutually exclusive")
	}
	if len(form.Fields) > 0 && p.Response != nil {
		return fmt.Errorf("if_unanswered: use fields for a multi-question form")
	}
	if (form.Question != "" || form.ResponseSchema != nil) && p.Fields != nil {
		return fmt.Errorf("if_unanswered: use response for a single question or structured form")
	}
	return nil
}

func prepareUnanswered(deps ops.OpDependencies, ctx context.Context, form InputForm, policy UnansweredPolicy) (InputForm, error) {
	duration, err := time.ParseDuration(policy.After)
	if err != nil || duration <= 0 {
		return InputForm{}, fmt.Errorf("if_unanswered.after must be a positive duration")
	}
	if form.RequestID == "" {
		form.RequestID = uuid.NewString()
	}
	// Snapshot caller-owned values before validation and persistence.
	raw, err := json.Marshal(FallbackAnswers{Response: policy.Response, Fields: policy.Fields, ArtifactRefs: policy.ArtifactRefs})
	if err != nil {
		return InputForm{}, err
	}
	if err := json.Unmarshal(raw, &form.Fallback); err != nil {
		return InputForm{}, err
	}
	prepared := time.Now().UTC()
	at := prepared.Add(duration)
	form.FallbackAt = at.Format(time.RFC3339Nano)
	if _, err := acceptFallback(deps, ctx, form); err != nil {
		return InputForm{}, fmt.Errorf("if_unanswered: %w", err)
	}
	if err := ops.SetNextTaskAlternate(deps, jobworkflow.TaskAlternate{TaskType: alternateTaskType, At: at}); err != nil {
		return InputForm{}, err
	}
	return form, nil
}

// GetAlternateOp registers the internal worker implementing pending input collection.
func GetAlternateOp() ops.RegisterableOp {
	op, err := ops.NewOp().WithType("input-alternate").WithAcceptsArtifacts(true).
		AddStep("complete", ops.NewStepWithDeps(completeUnanswered)).Build()
	if err != nil {
		panic(err)
	}
	return op
}

func completeUnanswered(deps ops.OpDependencies, ctx context.Context, form InputForm) (map[string]any, error) {
	at, err := time.Parse(time.RFC3339Nano, form.FallbackAt)
	if err != nil || time.Now().Before(at) {
		return nil, fmt.Errorf("input alternate is not eligible")
	}
	out, err := acceptFallback(deps, ctx, form)
	if err != nil {
		return nil, err
	}
	value, err := jsonValue(out)
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}

func acceptFallback(deps ops.OpDependencies, ctx context.Context, form InputForm) (Output, error) {
	if form.Fallback == nil || form.RequestID == "" {
		return Output{}, fmt.Errorf("input alternate requires frozen answers and request identity")
	}
	b := newArtifactBinder(ctx, deps, nil)
	defer b.cleanup()
	answers := form.Fallback
	actor := Actor{ID: "input-alternate", Kind: "automation"}
	submissionID := "if-unanswered:" + form.RequestID
	if form.ResponseSchema != nil {
		return acceptStructured(form, StructuredSubmission{RequestID: form.RequestID, SubmissionID: submissionID, Response: answers.Response, ArtifactRefs: answers.ArtifactRefs}, actor, b)
	}
	// Both review and ordinary forms share defaults, type/choice validation, and
	// stored attachment binding. Recipe fallback values cannot contain uploads.
	return acceptForm(form, FormSubmission{RequestID: form.RequestID, SubmissionID: submissionID, Response: answers.Response, Fields: answers.Fields, ArtifactRefs: answers.ArtifactRefs}, actor, b)
}
