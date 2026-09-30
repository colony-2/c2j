package input

import "fmt"

// ValidationError reports input-op invariants that cannot be expressed safely
// with field tags.
type ValidationError struct {
	Field    string
	Message  string
	Required bool
}

func (e ValidationError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

func (e ValidationError) RequiredValidationError() bool {
	return e.Required
}

func (in Input) ValidateOpInput() error {
	if err := in.Form.ValidateOpInput(); err != nil {
		return err
	}
	if in.IfUnanswered != nil {
		return in.IfUnanswered.validate(in.Form)
	}
	return nil
}

func (c Config) ValidateOpInput() error {
	if c.Kind != "" && c.Kind != "review" {
		return ValidationError{Field: "form.kind", Message: "must be review when supplied"}
	}
	if c.Kind == "review" {
		if c.Question != "" && len(c.Fields) > 0 {
			return ValidationError{Field: "form", Message: "use question or fields, not both"}
		}
		seen := map[string]bool{}
		for _, field := range c.Fields {
			if field.ID == "" || seen[field.ID] {
				return ValidationError{Field: "form.fields", Message: "question IDs must be nonempty and unique"}
			}
			seen[field.ID] = true
		}
		if c.ResponseSchema != nil || c.Request != nil || c.RequestSchema != nil || c.Presentation != nil {
			return ValidationError{Field: "form", Message: "review uses ordinary questions and fields, not structured schemas"}
		}
	} else if len(c.Documents) > 0 {
		return ValidationError{Field: "form.documents", Message: "requires kind: review"}
	}

	if c.ResponseSchema != nil {
		if c.Question != "" || c.Type != "" || len(c.Fields) > 0 || len(c.Options) > 0 || c.Scale != nil || c.Default != nil || c.Context.ArtifactsFromOutput != "" || len(c.Context.Artifacts) > 0 || len(c.Context.ArtifactsGlob) > 0 {
			return ValidationError{Field: "form", Message: "structured input cannot be mixed with ordinary form controls"}
		}
		return nil
	}
	if c.Request != nil || c.RequestSchema != nil || c.Presentation != nil {
		return ValidationError{Field: "form.response_schema", Message: "required for structured input", Required: true}
	}
	if c.Question == "" && len(c.Fields) == 0 {
		return ValidationError{Field: "form.question", Message: "question, fields, or response_schema is required", Required: true}
	}
	if choiceOptionsRequired(c.Type) && len(c.Options) == 0 {
		return ValidationError{
			Field:    "form.options",
			Message:  fmt.Sprintf("required for %s input fields", c.Type),
			Required: true,
		}
	}
	for i, field := range c.Fields {
		if choiceOptionsRequired(field.Type) && len(field.Options) == 0 {
			return ValidationError{
				Field:    fmt.Sprintf("form.fields[%d].options", i),
				Message:  fmt.Sprintf("required for %s input fields", field.Type),
				Required: true,
			}
		}
	}
	return nil
}

func choiceOptionsRequired(fieldType FieldType) bool {
	switch fieldType {
	case FieldTypeMultipleChoice, FieldTypeCheckboxes, FieldTypeDropdown:
		return true
	default:
		return false
	}
}
