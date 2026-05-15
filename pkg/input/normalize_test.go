package input

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeOutputDefaultsOptionalFields(t *testing.T) {
	out, err := NormalizeOutput(Config{
		Fields: []FormField{
			{ID: "summary", Type: FieldTypeShortAnswer},
			{ID: "description", Type: FieldTypeParagraphText, Default: "explicit"},
			{ID: "decision", Type: FieldTypeDropdown, Options: []Option{{Value: "go"}}},
			{ID: "targets", Type: FieldTypeCheckboxes, Options: []Option{{Value: "api"}}},
			{ID: "approved", Type: FieldTypeBoolean},
			{ID: "score", Type: FieldTypeLinearScale, Scale: &LinearScale{Min: 2, Max: 5}},
			{ID: "date", Type: FieldTypeDate},
			{ID: "time", Type: FieldTypeTime},
		},
	}, Output{Fields: map[string]interface{}{"approved": false}})
	if err != nil {
		t.Fatalf("NormalizeOutput returned error: %v", err)
	}

	want := map[string]interface{}{
		"summary":     "",
		"description": "explicit",
		"decision":    "",
		"targets":     []interface{}{},
		"approved":    false,
		"score":       2,
		"date":        "",
		"time":        "",
	}
	if !reflect.DeepEqual(out.Fields, want) {
		t.Fatalf("fields = %#v, want %#v", out.Fields, want)
	}
}

func TestNormalizeOutputPreservesSubmittedZeroValues(t *testing.T) {
	out, err := NormalizeOutput(Config{
		Fields: []FormField{
			{ID: "approved", Type: FieldTypeBoolean, Default: true},
			{ID: "score", Type: FieldTypeLinearScale, Scale: &LinearScale{Min: 3, Max: 5}},
			{ID: "targets", Type: FieldTypeCheckboxes, Options: []Option{{Value: "api"}}},
		},
	}, Output{Fields: map[string]interface{}{
		"approved": false,
		"score":    0,
		"targets":  []interface{}{},
	}})
	if err != nil {
		t.Fatalf("NormalizeOutput returned error: %v", err)
	}

	if out.Fields["approved"] != false {
		t.Fatalf("approved = %#v, want false", out.Fields["approved"])
	}
	if out.Fields["score"] != 0 {
		t.Fatalf("score = %#v, want 0", out.Fields["score"])
	}
	if !reflect.DeepEqual(out.Fields["targets"], []interface{}{}) {
		t.Fatalf("targets = %#v, want empty list", out.Fields["targets"])
	}
}

func TestNormalizeOutputRequiresMissingRequiredField(t *testing.T) {
	_, err := NormalizeOutput(Config{
		Fields: []FormField{{ID: "decision", Type: FieldTypeShortAnswer, Required: true}},
	}, Output{Fields: map[string]interface{}{}})
	if err == nil {
		t.Fatal("expected missing required field to fail")
	}
	if !strings.Contains(err.Error(), "decision") {
		t.Fatalf("error = %q, want field id", err)
	}
}

func TestNormalizeOutputAllowsExplicitDefaultForRequiredField(t *testing.T) {
	out, err := NormalizeOutput(Config{
		Fields: []FormField{{ID: "decision", Type: FieldTypeShortAnswer, Required: true, Default: "fallback"}},
	}, Output{Fields: map[string]interface{}{}})
	if err != nil {
		t.Fatalf("NormalizeOutput returned error: %v", err)
	}
	if out.Fields["decision"] != "fallback" {
		t.Fatalf("decision = %#v, want fallback", out.Fields["decision"])
	}
}

func TestNormalizeOutputSingleQuestionPopulatesResponseAndFields(t *testing.T) {
	tests := []struct {
		name         string
		out          Output
		wantResponse interface{}
		wantField    interface{}
	}{
		{
			name:         "response populates field",
			out:          Output{Response: "approved"},
			wantResponse: "approved",
			wantField:    "approved",
		},
		{
			name:         "field populates response",
			out:          Output{Fields: map[string]interface{}{"response": "field-value"}},
			wantResponse: "field-value",
			wantField:    "field-value",
		},
		{
			name:         "default populates both",
			out:          Output{},
			wantResponse: "fallback",
			wantField:    "fallback",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := NormalizeOutput(Config{
				Question: "Decision?",
				Type:     FieldTypeShortAnswer,
				Default:  "fallback",
			}, tc.out)
			if err != nil {
				t.Fatalf("NormalizeOutput returned error: %v", err)
			}
			if out.Response != tc.wantResponse {
				t.Fatalf("response = %#v, want %#v", out.Response, tc.wantResponse)
			}
			if out.Fields["response"] != tc.wantField {
				t.Fatalf("fields.response = %#v, want %#v", out.Fields["response"], tc.wantField)
			}
		})
	}
}

func TestNormalizeOutputMapUsesRenderedFormSchema(t *testing.T) {
	out, err := NormalizeOutputMap(map[string]interface{}{
		"form": map[string]interface{}{
			"title": "Review",
			"fields": []interface{}{
				map[string]interface{}{"id": "approved", "question": "Approved?", "type": "boolean"},
				map[string]interface{}{"id": "score", "question": "Score", "type": "linear_scale", "scale": map[string]interface{}{"min": 4, "max": 5}},
			},
		},
	}, map[string]interface{}{"fields": map[string]interface{}{}})
	if err != nil {
		t.Fatalf("NormalizeOutputMap returned error: %v", err)
	}

	fields, ok := out["fields"].(map[string]interface{})
	if !ok {
		t.Fatalf("fields = %#v, want map", out["fields"])
	}
	if fields["approved"] != false {
		t.Fatalf("approved = %#v, want false", fields["approved"])
	}
	if fields["score"] != 4 {
		t.Fatalf("score = %#v, want 4", fields["score"])
	}
}
