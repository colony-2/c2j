package input

import (
	"strings"
	"testing"
)

func TestValidateOpInputRequiresChoiceOptions(t *testing.T) {
	choiceTypes := []FieldType{FieldTypeMultipleChoice, FieldTypeCheckboxes, FieldTypeDropdown}

	for _, fieldType := range choiceTypes {
		t.Run("single/"+string(fieldType), func(t *testing.T) {
			err := (Input{Form: Config{Question: "Continue?", Type: fieldType}}).ValidateOpInput()
			if err == nil {
				t.Fatal("expected missing choice options to fail")
			}
			if !strings.Contains(err.Error(), "form.options") {
				t.Fatalf("error = %q, want form.options", err)
			}
		})

		t.Run("field/"+string(fieldType), func(t *testing.T) {
			err := (Input{Form: Config{Fields: []FormField{
				{ID: "decision", Question: "Continue?", Type: fieldType},
			}}}).ValidateOpInput()
			if err == nil {
				t.Fatal("expected missing choice options to fail")
			}
			if !strings.Contains(err.Error(), "form.fields[0].options") {
				t.Fatalf("error = %q, want form.fields[0].options", err)
			}
		})
	}
}

func TestValidateOpInputAllowsChoiceOptions(t *testing.T) {
	opts := []Option{{Value: "continue", Label: "Continue"}}

	cases := []Input{
		{Form: Config{Question: "Continue?", Type: FieldTypeMultipleChoice, Options: opts}},
		{Form: Config{Fields: []FormField{
			{ID: "decision", Question: "Continue?", Type: FieldTypeDropdown, Options: opts},
		}}},
	}

	for _, tc := range cases {
		if err := tc.ValidateOpInput(); err != nil {
			t.Fatalf("ValidateOpInput returned error: %v", err)
		}
	}
}

func TestValidateOpInputAllowsNonChoiceInputsWithoutOptions(t *testing.T) {
	inputs := []Input{
		{Form: Config{Question: "Describe it", Type: FieldTypeParagraphText}},
		{Form: Config{Fields: []FormField{
			{ID: "description", Question: "Describe it", Type: FieldTypeParagraphText},
		}}},
	}

	for _, in := range inputs {
		if err := in.ValidateOpInput(); err != nil {
			t.Fatalf("ValidateOpInput returned error: %v", err)
		}
	}
}
