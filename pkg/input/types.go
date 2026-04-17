package input

import (
	"time"

	openapi "github.com/colony-2/c2j/pkg/input/openapi"
)

// FieldType represents the type of form field
type FieldType string

const (
	FieldTypeShortAnswer        FieldType = "short_answer"
	FieldTypeParagraphText      FieldType = "paragraph_text"
	FieldTypeMultipleChoice     FieldType = "multiple_choice"
	FieldTypeCheckboxes         FieldType = "checkboxes"
	FieldTypeDropdown           FieldType = "dropdown"
	FieldTypeLinearScale        FieldType = "linear_scale"
	FieldTypeMultipleChoiceGrid FieldType = "multiple_choice_grid"
	FieldTypeCheckboxGrid       FieldType = "checkbox_grid"
	FieldTypeDate               FieldType = "date"
	FieldTypeTime               FieldType = "time"
	FieldTypeFileUpload         FieldType = "file_upload"
)

// Option represents a choice option for fields like multiple choice, dropdown, etc.
type Option struct {
	Value string `json:"value" validate:"required" jsonschema:"required,description=Option value"`
	Label string `json:"label,omitempty" jsonschema:"description=Optional display label"`
}

// LinearScale represents configuration for linear scale fields
type LinearScale struct {
	Min      int    `json:"min" validate:"required,gte=0,lte=10" jsonschema:"required,minimum=0,maximum=10,description=Minimum scale value"`
	Max      int    `json:"max" validate:"required,gte=1,lte=10" jsonschema:"required,minimum=1,maximum=10,description=Maximum scale value"`
	MinLabel string `json:"min_label,omitempty" jsonschema:"description=Label for minimum value"`
	MaxLabel string `json:"max_label,omitempty" jsonschema:"description=Label for maximum value"`
}

// FieldValidation represents validation rules for a form field
type FieldValidation struct {
	MinLength int    `json:"min_length,omitempty" jsonschema:"minimum=0,description=Minimum length for text fields"`
	MaxLength int    `json:"max_length,omitempty" jsonschema:"minimum=1,description=Maximum length for text fields"`
	Pattern   string `json:"pattern,omitempty" jsonschema:"description=Regex pattern for validation"`
	Min       int    `json:"min,omitempty" jsonschema:"description=Minimum value for numeric fields"`
	Max       int    `json:"max,omitempty" jsonschema:"description=Maximum value for numeric fields"`
}

// FormField represents a single field in a multi-field form
type FormField struct {
	ID          string          `json:"id" validate:"required" jsonschema:"required,description=Unique field identifier"`
	Type        FieldType       `json:"type" validate:"required,oneof=short_answer paragraph_text multiple_choice checkboxes dropdown linear_scale multiple_choice_grid checkbox_grid date time file_upload" jsonschema:"required,enum=short_answer|paragraph_text|multiple_choice|checkboxes|dropdown|linear_scale|multiple_choice_grid|checkbox_grid|date|time|file_upload,description=Field type"`
	Question    string          `json:"question" validate:"required" jsonschema:"required,description=Field question or label"`
	Required    bool            `json:"required,omitempty" jsonschema:"description=Whether field is required"`
	Placeholder string          `json:"placeholder,omitempty" jsonschema:"description=Placeholder text"`
	Options     []Option        `json:"options,omitempty" validate:"omitempty,required_if=Type multiple_choice required_if=Type checkboxes required_if=Type dropdown,min=1,dive" jsonschema:"description=Options for choice fields"`
	Scale       *LinearScale    `json:"scale,omitempty" validate:"required_if=Type linear_scale" jsonschema:"description=Configuration for linear scale fields"`
	Validation  FieldValidation `json:"validation,omitempty" jsonschema:"description=Field validation rules"`
}

// FormContext represents context information for the form
type FormContext struct {
	Artifacts           []Artifact    `json:"artifacts,omitempty" jsonschema:"description=Static artifact references"`
	ArtifactsFromOutput string        `json:"artifacts_from_output,omitempty" jsonschema:"description=Reference to artifacts from previous activity output"`
	ArtifactsGlob       []GlobPattern `json:"artifacts_glob,omitempty" jsonschema:"description=Glob patterns for artifact discovery"`
}

// Artifact represents a static artifact reference
type Artifact struct {
	Path string `json:"path" validate:"required" jsonschema:"required,description=Path to artifact file"`
}

// GlobPattern represents a glob pattern for artifact discovery
type GlobPattern struct {
	Pattern string `json:"pattern" validate:"required" jsonschema:"required,description=Glob pattern for matching files"`
}

// InputForm represents the complete form structure
type InputForm struct {
	// Single question fields
	Question string       `json:"question,omitempty" jsonschema:"description=Single question text"`
	Type     FieldType    `json:"type,omitempty" jsonschema:"enum=short_answer|paragraph_text|multiple_choice|checkboxes|dropdown|linear_scale|date|time,description=Field type for single question"`
	Options  []Option     `json:"options,omitempty" jsonschema:"description=Options for single choice field"`
	Scale    *LinearScale `json:"scale,omitempty" jsonschema:"description=Configuration for linear scale fields"`

	// Multi-field form
	Title  string      `json:"title,omitempty" jsonschema:"description=Form title"`
	Fields []FormField `json:"fields,omitempty" jsonschema:"description=Form fields for multi-field form"`

	// Common fields
	Context FormContext   `json:"context,omitempty" jsonschema:"description=Form context and artifacts"`
	Output  *Output       `json:"output,omitempty" jsonschema:"description=Optional auto-fill output response"`
	Timeout time.Duration `json:"timeout,omitempty" jsonschema:"default=300,description=Timeout in seconds"`
}

// FormResponse represents the user's response to a form (OpenAPI-generated type).
type FormResponse = openapi.FormResponse

// APIFormField is the generated transport shape used by user-input clients.
type APIFormField = openapi.FormField

// APIOption is the generated transport shape for select options.
type APIOption = openapi.Option

// APIFieldType is the generated transport enum for form fields.
type APIFieldType = openapi.FieldType

const (
	APIFieldTypeCheckboxes  = openapi.FieldTypeCheckboxes
	APIFieldTypeLinearScale = openapi.FieldTypeLinearScale
)

// InputWorkflowParams represents parameters for the input collection workflow
type InputWorkflowParams struct {
	ID         string        `json:"id"`
	Form       InputForm     `json:"form"`
	Timeout    time.Duration `json:"timeout"`
	BoxID      string        `json:"box_id"`
	ActivityID string        `json:"activity_id"`
}

// InputWorkflowResult represents the result of the input collection workflow
type InputWorkflowResult struct {
	FormResponse map[string]interface{} `json:"form_response"`
	UserID       string                 `json:"user_id"`
	Metadata     map[string]interface{} `json:"metadata"`
}

// UserResponseSignal represents the signal sent when a user responds
type UserResponseSignal struct {
	Fields      map[string]interface{} `json:"fields"`
	UserID      string                 `json:"user_id"`
	RespondedAt time.Time              `json:"responded_at"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// PendingInput represents a pending input request (OpenAPI-generated type).
type PendingInput = openapi.PendingInput

// UserInputDetails describes a pending input request in a transport-agnostic shape.
type UserInputDetails = openapi.UserInputDetails

// InputRequestSignal represents a signal sent by the activity to request user input
type InputRequestSignal struct {
	ActivityID string        `json:"activity_id"`
	WorkflowID string        `json:"workflow_id"`
	Form       InputForm     `json:"form"`
	Timeout    time.Duration `json:"timeout"`
}

// InputCancelSignal represents a signal to cancel a pending input request
type InputCancelSignal struct {
	ActivityID string `json:"activity_id"`
	ResponseID string `json:"response_id"`
	Reason     string `json:"reason"`
}

// InputResponseSignal represents a signal sent when user submits form
type InputResponseSignal struct {
	ActivityID   string       `json:"activity_id"`
	ResponseID   string       `json:"response_id"`
	FormResponse FormResponse `json:"form_response"`
	UserID       string       `json:"user_id"`
	RespondedAt  time.Time    `json:"responded_at"`
}

// InputTimeoutSignal represents a signal sent when input request times out
type InputTimeoutSignal struct {
	ActivityID string    `json:"activity_id"`
	ResponseID string    `json:"response_id"`
	TimedOutAt time.Time `json:"timed_out_at"`
}
