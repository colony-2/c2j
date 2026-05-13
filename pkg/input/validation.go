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
	return in.Form.ValidateOpInput()
}

func (c Config) ValidateOpInput() error {
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
