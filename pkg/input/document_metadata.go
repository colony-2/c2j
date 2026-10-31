package input

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"
)

// DocumentMetadata describes an attached review document. It grants no authority
// and never replaces the document's immutable reference or original bytes.
type DocumentMetadata struct {
	Title             string   `json:"title,omitempty" jsonschema:"maxLength=120,description=Plain text review document title"`
	Description       string   `json:"description,omitempty" jsonschema:"maxLength=300,description=Plain text review document description"`
	Role              string   `json:"role,omitempty" jsonschema:"enum=,enum=primary,enum=supporting,enum=diagnostic,description=Optional descriptive role; empty is neutral"`
	Order             *int     `json:"order,omitempty" jsonschema:"minimum=0,maximum=1000000,description=Optional display order; zero differs from absence"`
	SourceDocumentIDs []string `json:"source_document_ids,omitempty" jsonschema:"maxItems=128,description=Declared source IDs in the same documents map; do not recursively expand"`
}

// DecodeFromMap implements ops.MapDecoder locally: mapstructure's default
// float-to-int conversion would silently truncate fractional producer values.
// This is type decoding only; producer validation is not run on frozen forms.
func (m *DocumentMetadata) DecodeFromMap(input any) error {
	// Inspect supported map and typed inputs, including pointer/string aliases,
	// before encoding/json can replace malformed UTF-8 with U+FFFD.
	value := reflect.ValueOf(input)
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		value = value.Elem()
	}
	for _, field := range []struct{ json, goName string }{{"title", "Title"}, {"description", "Description"}} {
		var text reflect.Value
		if value.IsValid() {
			switch value.Kind() {
			case reflect.Map:
				if value.Type().Key().Kind() == reflect.String {
					text = value.MapIndex(reflect.ValueOf(field.json).Convert(value.Type().Key()))
				}
			case reflect.Struct:
				text = value.FieldByName(field.goName)
			}
		}
		for text.IsValid() && (text.Kind() == reflect.Pointer || text.Kind() == reflect.Interface) {
			text = text.Elem()
		}
		if text.IsValid() && text.Kind() == reflect.String && !utf8.ValidString(text.String()) {
			return ValidationError{Field: "document_metadata." + field.json, Message: "must be valid UTF-8"}
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return m.UnmarshalJSON(raw)
}

// UnmarshalJSON also rejects malformed UTF-8 arriving directly as JSON.
func (m *DocumentMetadata) UnmarshalJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return ValidationError{Field: "document_metadata", Message: "must be valid UTF-8"}
	}
	type plain DocumentMetadata
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		field := "document_metadata"
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) && typeError.Field != "" {
			field += "." + typeError.Field
		}
		return ValidationError{Field: field, Message: err.Error(), Cause: err}
	}
	*m = DocumentMetadata(value)
	return nil
}

type reviewDocumentPresentation struct {
	DocumentMetadata   map[string]DocumentMetadata `json:"document_metadata,omitempty"`
	OverviewDocumentID string                      `json:"overview_document_id,omitempty"`
}

func (c Config) validateDocumentPresentation() error {
	invalid := func(field, message string) error { return ValidationError{Field: "form." + field, Message: message} }
	if c.Kind != "review" {
		if len(c.DocumentMetadata) > 0 {
			return invalid("document_metadata", "requires kind: review")
		}
		if c.OverviewDocumentID != "" {
			return invalid("overview_document_id", "requires kind: review")
		}
		return nil
	}
	if c.OverviewDocumentID != "" {
		if _, ok := c.Documents[c.OverviewDocumentID]; !ok {
			return invalid("overview_document_id", "must match an attached document ID")
		}
	}
	for id, metadata := range c.DocumentMetadata {
		field := "document_metadata." + id
		if _, ok := c.Documents[id]; !ok {
			return invalid(field, "must match an attached document ID")
		}
		for _, text := range []struct {
			name, value string
			limit       int
		}{{"title", metadata.Title, 120}, {"description", metadata.Description, 300}} {
			if !utf8.ValidString(text.value) {
				return invalid(field+"."+text.name, "must be valid UTF-8")
			}
			if utf8.RuneCountInString(text.value) > text.limit {
				return invalid(field+"."+text.name, fmt.Sprintf("must contain at most %d Unicode code points", text.limit))
			}
			for _, r := range text.value {
				if r < 32 || r == 127 {
					return invalid(field+"."+text.name, "must not contain control characters")
				}
			}
		}
		switch metadata.Role {
		case "", "primary", "supporting", "diagnostic":
		default:
			return invalid(field+".role", "must be primary, supporting, diagnostic, or empty")
		}
		if metadata.Order != nil && (*metadata.Order < 0 || *metadata.Order > 1000000) {
			return invalid(field+".order", "must be an integer between 0 and 1000000")
		}
		if len(metadata.SourceDocumentIDs) > 128 {
			return invalid(field+".source_document_ids", "must contain at most 128 IDs")
		}
		seen := make(map[string]bool, len(metadata.SourceDocumentIDs))
		for i, source := range metadata.SourceDocumentIDs {
			sourceField := fmt.Sprintf("%s.source_document_ids[%d]", field, i)
			if source == id {
				return invalid(sourceField, "must not reference itself")
			}
			if seen[source] {
				return invalid(sourceField, "duplicate source ID")
			}
			if _, ok := c.Documents[source]; !ok {
				return invalid(sourceField, "must match an attached document ID")
			}
			seen[source] = true
		}
	}
	raw, err := json.Marshal(reviewDocumentPresentation{c.DocumentMetadata, c.OverviewDocumentID})
	if err != nil {
		return invalid("document_metadata", err.Error())
	}
	if len(raw) > 65536 {
		return invalid("document_metadata", "combined document presentation must not exceed 65536 JSON bytes")
	}
	return nil
}
