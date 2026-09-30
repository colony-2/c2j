package input

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/google/uuid"
)

// FormSubmission answers a review using ordinary form fields and attachments.
// File-upload fields may contain stored refs or jobdb.Artifact values. Additional
// documents may be supplied as named, already-stored ArtifactRefs.
type FormSubmission struct {
	RequestID    string                   `json:"request_id"`
	SubmissionID string                   `json:"submission_id"`
	Response     any                      `json:"response,omitempty"`
	Fields       map[string]any           `json:"fields,omitempty"`
	ArtifactRefs map[string]artifacts.Ref `json:"artifact_refs,omitempty"`
}

func buildReviewForm(deps ops.OpDependencies, ctx context.Context, config Config) (InputForm, error) {
	b := newArtifactBinder(ctx, deps, nil)
	documents := make(map[string]artifacts.Ref, len(config.Documents))
	for id, value := range config.Documents {
		if strings.TrimSpace(id) == "" {
			return InputForm{}, fmt.Errorf("form.documents: document ID is required")
		}
		ref, err := storedDocument(value)
		if err == nil {
			err = b.checkRef(ref)
		}
		if err != nil {
			return InputForm{}, fmt.Errorf("form.documents.%s: %w", id, err)
		}
		documents[id] = ref
	}
	// Use the existing form construction and internal wait, without another op.
	ordinary := config
	ordinary.Kind, ordinary.Documents = "", nil
	form, err := buildForm(deps, ctx, Input{Form: ordinary})
	if err != nil {
		return InputForm{}, err
	}
	form.Kind, form.RequestID, form.Documents = "review", uuid.NewString(), documents
	// Freeze caller-owned field definitions and references before publication.
	raw, err := json.Marshal(form)
	if err != nil {
		return InputForm{}, err
	}
	form = InputForm{}
	err = json.Unmarshal(raw, &form)
	return form, err
}

// storedDocument also accepts existing bare JobDB artifact keys used in recipe
// artifact maps. It never accepts paths, URLs, or inline file content.
func storedDocument(value any) (artifacts.Ref, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return artifacts.Ref{}, err
	}
	var ref artifacts.Ref
	if err := json.Unmarshal(raw, &ref); err != nil {
		return ref, err
	}
	if ref.Kind == "" {
		var key jobdb.ArtifactKey
		if err := json.Unmarshal(raw, &key); err != nil {
			return ref, err
		}
		ref = artifacts.NewStoredRef(key)
	}
	if ref.Kind != artifacts.RefKindStored {
		return ref, fmt.Errorf("document requires a stored artifact reference")
	}
	return ref, ref.Validate()
}

// SubmitFormResponse validates and completes the exact pending review. The
// embedding application supplies the authenticated actor separately.
func (r *Runtime) SubmitFormResponse(ctx context.Context, tenantID, jobID string, submission FormSubmission, actor Actor) (Output, error) {
	return r.submitInput(ctx, tenantID, jobID, func(form InputForm, b *artifactBinder) (Output, error) {
		return acceptReview(form, submission, actor, b)
	})
}

func acceptReview(form InputForm, sub FormSubmission, actor Actor, b *artifactBinder) (Output, error) {
	if form.Kind != "review" {
		return Output{}, fmt.Errorf("input is not a review")
	}
	if form.RequestID == "" || sub.RequestID != form.RequestID {
		return Output{}, fmt.Errorf("request_id: does not match the pending input")
	}
	return acceptForm(form, sub, actor, b)
}

// acceptForm is shared by review submissions and automatic answers to ordinary forms.
func acceptForm(form InputForm, sub FormSubmission, actor Actor, b *artifactBinder) (Output, error) {
	if strings.TrimSpace(sub.SubmissionID) == "" {
		return Output{}, fmt.Errorf("submission_id: is required")
	}
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.Kind) == "" {
		return Output{}, fmt.Errorf("actor: id and kind are required")
	}
	if len(form.Fields) > 0 && sub.Response != nil {
		return Output{}, fmt.Errorf("response: use fields to answer a multi-question review")
	}
	cfg := Config{Question: form.Question, Type: form.Type, Options: form.Options, Scale: form.Scale, Default: form.Default, Fields: form.Fields}
	out, err := NormalizeOutput(cfg, Output{Response: sub.Response, Fields: sub.Fields, UserID: actor.ID})
	if err != nil {
		return Output{}, err
	}
	fields := form.Fields
	if form.Question != "" {
		fields = []FormField{{ID: "response", Type: form.Type, Options: form.Options}}
	}
	known := map[string]bool{}
	for _, field := range fields {
		known[field.ID] = true
		value, exists := out.Fields[field.ID]
		if !exists {
			continue
		}
		if value == nil {
			return Output{}, fmt.Errorf("fields.%s: answer cannot be null", field.ID)
		}
		if field.Type == FieldTypeFileUpload {
			if _, ok := value.(jobdb.Artifact); !ok {
				value, err = storedDocument(value)
				if err != nil {
					return Output{}, fmt.Errorf("fields.%s: %w", field.ID, err)
				}
			}
			value, err = b.bind(value)
			if err != nil {
				return Output{}, fmt.Errorf("fields.%s: %w", field.ID, err)
			}
			out.Fields[field.ID] = value
		} else if err := validateAnswer(field, value); err != nil {
			return Output{}, err
		}
	}
	for id := range out.Fields {
		if !known[id] {
			return Output{}, fmt.Errorf("fields.%s: unknown question", id)
		}
	}
	for name, ref := range sub.ArtifactRefs {
		if err := b.checkRef(ref); err != nil {
			return Output{}, err
		}
		if err := b.addRef(name, ref); err != nil {
			return Output{}, err
		}
	}
	out.ArtifactRefs = b.refs
	out.Receipt = &Receipt{RequestID: form.RequestID, SubmissionID: sub.SubmissionID, SubmittedAt: time.Now().UTC(), Actor: actor}
	return out, nil
}

func validateAnswer(field FormField, value any) error {
	invalid := func() error { return fmt.Errorf("fields.%s: invalid %s answer", field.ID, field.Type) }
	switch field.Type {
	case FieldTypeShortAnswer, FieldTypeParagraphText, FieldTypeDate, FieldTypeTime:
		if _, ok := value.(string); !ok {
			return invalid()
		}
	case FieldTypeBoolean:
		if _, ok := value.(bool); !ok {
			return invalid()
		}
	case FieldTypeMultipleChoice, FieldTypeDropdown:
		for _, option := range field.Options {
			if value == option.Value {
				return nil
			}
		}
		// Optional choice fields use the existing empty-string default.
		if value == "" && !field.Required {
			return nil
		}
		return invalid()
	case FieldTypeCheckboxes:
		raw, err := jsonValue(value)
		if err != nil {
			return invalid()
		}
		values, ok := raw.([]any)
		if !ok {
			return invalid()
		}
		for _, v := range values {
			choice := field
			choice.Type, choice.Required = FieldTypeMultipleChoice, true
			if err := validateAnswer(choice, v); err != nil {
				return invalid()
			}
		}
	}
	return nil
}

func autoFillReview(deps ops.OpDependencies, ctx context.Context, form InputForm) (Output, error) {
	if form.Output == nil {
		return Output{}, fmt.Errorf("review autofill requires an output")
	}
	if form.Output.Receipt != nil || form.Output.UserID != "" || len(form.Output.Metadata) > 0 {
		return Output{}, fmt.Errorf("review autofill accepts response, fields, and artifact_refs only")
	}
	b := newArtifactBinder(ctx, deps, nil)
	transferred := false
	defer func() {
		if !transferred {
			b.cleanup()
		}
	}()
	out, err := acceptReview(form, FormSubmission{RequestID: form.RequestID, SubmissionID: "autofill:" + form.RequestID, Response: form.Output.Response, Fields: form.Output.Fields, ArtifactRefs: form.Output.ArtifactRefs}, Actor{ID: "autofill", Kind: "automation"}, b)
	if err != nil {
		return Output{}, err
	}
	for _, a := range b.artifacts {
		if err := deps.AddOutputArtifact(a); err != nil {
			return Output{}, err
		}
	}
	transferred = true
	return out, nil
}
