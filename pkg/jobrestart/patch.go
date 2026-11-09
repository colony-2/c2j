package jobrestart

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/task"
)

// ParsePatch validates only this CLI/helper input contract; the engine's
// historical, permissive merge behavior remains unchanged for other callers.
func ParsePatch(raw []byte) (*task.ContextPatch, error) {
	var document map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(&document); err != nil {
		return nil, fmt.Errorf("patch must be one JSON object: %w", err)
	}
	if document == nil {
		return nil, fmt.Errorf("patch must be one JSON object")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("patch must contain exactly one JSON document")
	}
	if err := validateTypedJSON(raw, reflect.TypeFor[task.ContextPatch](), "patch"); err != nil {
		return nil, err
	}
	var patch task.ContextPatch
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&patch); err != nil {
		return nil, fmt.Errorf("invalid context patch: %w", err)
	}
	if err := ValidatePatch(&patch); err != nil {
		return nil, err
	}
	return &patch, nil
}
func ValidatePatch(patch *task.ContextPatch) error {
	for _, key := range []string{"workspace", "cell_resolution"} {
		if _, exists := patch.Job[key]; exists {
			return fmt.Errorf("job.%s cannot be patched", key)
		}
	}
	if patch.Job != nil {
		raw, err := json.Marshal(patch.Job)
		if err != nil {
			return err
		}
		if err := validateTypedJSON(raw, reflect.TypeFor[contextual.JobContext](), "job"); err != nil {
			return err
		}
		var job contextual.JobContext
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&job); err != nil {
			return fmt.Errorf("invalid job patch: %w", err)
		}
	}
	for _, scope := range patch.Scopes {
		if scope.Container != "sequence" && scope.Container != "states" {
			return fmt.Errorf("scope container must be sequence or states")
		}
		if strings.TrimSpace(scope.ID) == "" {
			return fmt.Errorf("scope local step ID must not be blank")
		}
	}
	return nil
}

// Walk typed structures explicitly: custom JSON unmarshallers must not silently
// discard unsupported keys. Map keys remain extensible, map values stay typed.
func validateTypedJSON(raw json.RawMessage, typ reflect.Type, path string) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	} // merge deletion
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return fmt.Errorf("%s must be an object: %w", path, err)
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
		}
		for key, value := range object {
			field, ok := fields[key]
			if !ok {
				return fmt.Errorf("unsupported patch field %s.%s", path, key)
			}
			if err := validateTypedJSON(value, field, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Map:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return fmt.Errorf("%s must be an object: %w", path, err)
		}
		for key, value := range object {
			if err := validateTypedJSON(value, typ.Elem(), path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		// RawMessage and byte slices have their own JSON encoding.
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			return fmt.Errorf("%s must be an array: %w", path, err)
		}
		for _, value := range array {
			if err := validateTypedJSON(value, typ.Elem(), path+"[]"); err != nil {
				return err
			}
		}
	}
	return nil
}
