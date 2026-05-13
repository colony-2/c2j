package compiler

import (
	"reflect"
	"strings"
	"testing"

	inputop "github.com/colony-2/c2j/pkg/input"
)

func TestValidateOpInputTypeAcceptsInputChoiceOptions(t *testing.T) {
	choiceTypes := []string{"multiple_choice", "checkboxes", "dropdown"}
	formats := []struct {
		name string
		raw  func(string, []interface{}) map[string]interface{}
	}{
		{name: "single question", raw: singleChoiceQuestionInput},
		{name: "multi field", raw: multiFieldChoiceInput},
	}

	for _, format := range formats {
		for _, choiceType := range choiceTypes {
			t.Run(format.name+"/"+choiceType, func(t *testing.T) {
				err := validateOpInputType(reflect.TypeOf(inputop.Input{}), format.raw(choiceType, validChoiceOptions()), false)
				if err != nil {
					t.Fatalf("validateOpInputType returned error: %v", err)
				}
			})
		}
	}
}

func TestValidateOpInputTypeRequiresInputChoiceOptions(t *testing.T) {
	choiceTypes := []string{"multiple_choice", "checkboxes", "dropdown"}
	formats := []struct {
		name string
		raw  func(string, []interface{}) map[string]interface{}
	}{
		{name: "single question", raw: singleChoiceQuestionInput},
		{name: "multi field", raw: multiFieldChoiceInput},
	}

	for _, format := range formats {
		for _, choiceType := range choiceTypes {
			t.Run(format.name+"/"+choiceType, func(t *testing.T) {
				err := validateOpInputType(reflect.TypeOf(inputop.Input{}), format.raw(choiceType, nil), false)
				if err == nil {
					t.Fatal("expected missing choice options to fail validation")
				}
				if !strings.Contains(err.Error(), "options") || !strings.Contains(err.Error(), "required") {
					t.Fatalf("error = %q, want required options error", err)
				}
			})
		}
	}
}

func TestValidateOpInputTypeFiltersChoiceOptionRequirednessInValidateMode(t *testing.T) {
	err := validateOpInputType(reflect.TypeOf(inputop.Input{}), singleChoiceQuestionInput("multiple_choice", nil), true)
	if err != nil {
		t.Fatalf("validate mode should filter choice option requiredness, got: %v", err)
	}
}

func TestValidateOpInputTypeStillValidatesChoiceOptionValues(t *testing.T) {
	err := validateOpInputType(reflect.TypeOf(inputop.Input{}), singleChoiceQuestionInput("multiple_choice", []interface{}{
		map[string]interface{}{"label": "Missing value"},
	}), false)
	if err == nil {
		t.Fatal("expected empty option value to fail validation")
	}
	if !strings.Contains(err.Error(), "Value") || !strings.Contains(err.Error(), "required") {
		t.Fatalf("error = %q, want required option value", err)
	}
}

func TestValidateOpInputTypeAllowsNonChoiceInputWithoutOptions(t *testing.T) {
	err := validateOpInputType(reflect.TypeOf(inputop.Input{}), map[string]interface{}{
		"form": map[string]interface{}{
			"question": "Describe the change",
			"type":     "paragraph_text",
		},
	}, false)
	if err != nil {
		t.Fatalf("validateOpInputType returned error: %v", err)
	}
}

func singleChoiceQuestionInput(choiceType string, options []interface{}) map[string]interface{} {
	form := map[string]interface{}{
		"question": "Pick an option",
		"type":     choiceType,
	}
	if options != nil {
		form["options"] = options
	}
	return map[string]interface{}{"form": form}
}

func multiFieldChoiceInput(choiceType string, options []interface{}) map[string]interface{} {
	field := map[string]interface{}{
		"id":       "decision",
		"question": "Pick an option",
		"type":     choiceType,
	}
	if options != nil {
		field["options"] = options
	}
	return map[string]interface{}{
		"form": map[string]interface{}{
			"title":  "Decision",
			"fields": []interface{}{field},
		},
	}
}

func validChoiceOptions() []interface{} {
	return []interface{}{
		map[string]interface{}{"value": "continue", "label": "Continue"},
		map[string]interface{}{"value": "cancel", "label": "Cancel"},
	}
}
