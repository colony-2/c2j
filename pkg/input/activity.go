package input

import (
	"context"
	"reflect"

	"github.com/colony-2/c2j/pkg/ops"
)

// Config represents the configuration for the input activity
type Config struct {
	// Single question format
	Question string       `json:"question,omitempty" validate:"required_without=Fields" jsonschema:"description=Question to ask the user"`
	Type     FieldType    `json:"type,omitempty" validate:"omitempty,oneof=short_answer paragraph_text multiple_choice checkboxes dropdown linear_scale date time" jsonschema:"enum=short_answer|paragraph_text|multiple_choice|checkboxes|dropdown|linear_scale|date|time,description=Input field type"`
	Options  []Option     `json:"options,omitempty" validate:"omitempty,required_if=Type multiple_choice required_if=Type checkboxes required_if=Type dropdown,min=1,dive" jsonschema:"description=Options for choice fields"`
	Scale    *LinearScale `json:"scale,omitempty" validate:"required_if=Type linear_scale" jsonschema:"description=Configuration for linear scale fields"`

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
	Response interface{}            `json:"response,omitempty" jsonschema:"description=User response for single question"`
	Fields   map[string]interface{} `json:"fields,omitempty" jsonschema:"description=User responses for multi-field form"`
	UserID   string                 `json:"user_id,omitempty" jsonschema:"description=ID of user who responded"`
	Metadata map[string]interface{} `json:"metadata,omitempty" jsonschema:"description=Additional metadata"`
}

func GetOp() ops.RegisterableOp {
	op, err := ops.NewOp().
		WithType("input").
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
	form := InputForm{}

	// Check if it's a single question or multi-field form
	if config.Question != "" {
		// Single question format
		form.Question = config.Question
		form.Type = config.Type
		form.Options = config.Options
		form.Scale = config.Scale
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

	if !isZeroValue(out.Response) {
		return true
	}

	if len(out.Fields) > 0 {
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

func isZeroValue(val interface{}) bool {
	if val == nil {
		return true
	}

	rv := reflect.ValueOf(val)
	if !rv.IsValid() {
		return true
	}

	return rv.IsZero()
}
