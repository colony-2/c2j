package input

import (
	"context"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
)

// Config represents the configuration for the input activity
type Config struct {
	Kind      string         `json:"kind,omitempty" jsonschema:"enum=review,description=Optional review marker"`
	Documents map[string]any `json:"documents,omitempty"`
	// Structured mode. Schemas and request are frozen in the generate_form outcome.
	Request        any            `json:"request,omitempty"`
	RequestSchema  map[string]any `json:"request_schema,omitempty"`
	ResponseSchema map[string]any `json:"response_schema,omitempty"`
	Presentation   map[string]any `json:"presentation,omitempty"`

	// Single question format
	Question string       `json:"question,omitempty" jsonschema:"description=Question to ask the user"`
	Type     FieldType    `json:"type,omitempty" validate:"omitempty,oneof=short_answer paragraph_text multiple_choice checkboxes dropdown linear_scale boolean date time" jsonschema:"enum=short_answer|paragraph_text|multiple_choice|checkboxes|dropdown|linear_scale|boolean|date|time,description=Input field type"`
	Options  []Option     `json:"options,omitempty" validate:"omitempty,min=1,dive" jsonschema:"description=Options for choice fields"`
	Scale    *LinearScale `json:"scale,omitempty" validate:"required_if=Type linear_scale" jsonschema:"description=Configuration for linear scale fields"`
	Default  interface{}  `json:"default,omitempty" jsonschema:"description=Default value used when the response is omitted"`

	// Multi-field format
	Title   string      `json:"title,omitempty" jsonschema:"description=Form title"`
	Fields  []FormField `json:"fields,omitempty" validate:"omitempty,min=1,dive" jsonschema:"description=Form fields"`
	Context FormContext `json:"context,omitempty" jsonschema:"description=Form context and artifacts"`

	// Optional auto-fill response
	Output *Output `json:"autofill,omitempty" jsonschema:"description=Optional auto-fill output response"`
}

// Input represents the inputs passed to the input activity
type Input struct {
	Form Config `json:"form,omitempty" validate:"required" jsonschema:"description=Form formuration"`
}

// Output represents the output from the input activity
type Output struct {
	ArtifactRefs map[string]recipeartifacts.Ref `json:"artifact_refs"`
	Receipt      *Receipt                       `json:"receipt,omitempty"`
	Response     interface{}                    `json:"response" jsonschema:"description=User response for an ordinary or structured input"`
	Fields       map[string]interface{}         `json:"fields,omitempty" jsonschema:"description=User responses for multi-field form"`
	UserID       string                         `json:"user_id,omitempty" jsonschema:"description=ID of user who responded"`
	Metadata     map[string]interface{}         `json:"metadata,omitempty" jsonschema:"description=Additional metadata"`
}

func GetOp() ops.RegisterableOp {
	op, err := ops.NewOp().
		WithType("input").
		WithAcceptsArtifacts(true).
		WithManagementService(newInputManagementService()).
		AddStep("generate_form", ops.NewStepWithDeps(buildForm)).
		AddStep("collect_user_input", ops.NewNoTaskStep[InputForm, Output]()).
		Build()
	if err != nil {
		panic(err)
	}
	return op
}

// buildForm constructs the InputForm from config and input
func buildForm(deps ops.OpDependencies, ctx context.Context, in Input) (InputForm, error) {
	config := in.Form
	if err := config.ValidateOpInput(); err != nil {
		return InputForm{}, err
	}
	if config.Kind == "review" {
		return buildReviewForm(deps, ctx, config)
	}
	if config.ResponseSchema != nil {
		return buildStructuredForm(deps, ctx, config)
	}
	form := InputForm{}

	// Check if it's a single question or multi-field form
	if config.Question != "" {
		// Single question format
		form.Question = config.Question
		form.Type = config.Type
		form.Options = config.Options
		form.Scale = config.Scale
		form.Default = config.Default
	} else if len(config.Fields) > 0 {
		// Multi-field format
		form.Title = config.Title
		form.Fields = config.Fields
	}

	// Add context
	form.Context = config.Context
	if hasAutoFillValue(config.Output) {
		form.Output = config.Output
		deps.SetNextTaskType(autoFillTaskType)
	}

	// Process artifacts from input context if needed
	if form.Context.ArtifactsFromOutput != "" {
		// This would resolve artifacts from previous activity outputs
		// For now, we'll leave this as a placeholder
	}

	return form, nil
}

func hasAutoFillValue(out *Output) bool {
	if out == nil {
		return false
	}

	if out.Response != nil {
		return true
	}

	if out.Fields != nil {
		return true
	}

	if out.UserID != "" {
		return true
	}

	if len(out.Metadata) > 0 {
		return true
	}

	return false
}
