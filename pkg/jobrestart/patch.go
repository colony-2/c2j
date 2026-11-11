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

// ParsePatch is the shared public ingress contract. Stored legacy objects are
// decoded only by the runtime's persisted-payload reader.
func ParsePatch(raw []byte) (*task.ContextPatch, error) {
	var patch task.ContextPatch
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := d.Decode(&patch); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("patch must contain exactly one JSON document")
	}
	if err := ValidatePatch(&patch); err != nil {
		return nil, err
	}
	return &patch, nil
}

func ValidatePatch(patch *task.ContextPatch) error {
	if patch == nil {
		return nil
	}
	// Round-trip also validates operations constructed by Go callers.
	raw, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("invalid patch value")
	}
	var checked task.ContextPatch
	if err := json.Unmarshal(raw, &checked); err != nil {
		return err
	}
	for i, op := range checked {
		typ, err := PatchPathType(op.Path)
		if err != nil {
			return fmt.Errorf("operation %d path: %w", i, err)
		}
		if op.Op == "move" || op.Op == "copy" {
			if _, err := PatchPathType(op.From); err != nil {
				return fmt.Errorf("operation %d from: %w", i, err)
			}
			if op.Op == "move" && strings.HasPrefix(op.Path, op.From+"/") {
				return fmt.Errorf("operation %d cannot move into a descendant", i)
			}
		}
		if typ != nil && len(op.Value) > 0 {
			if err := ValidatePatchValue(op.Value, typ); err != nil {
				return fmt.Errorf("operation %d value: %w", i, err)
			}
		}
	}
	return nil
}

// PointerTokens decodes RFC 6901 exactly once, rejecting invalid escapes.
func PointerTokens(path string) ([]string, error) {
	if path == "" || !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("an editable JSON pointer is required")
	}
	tokens := strings.Split(path[1:], "/")
	for i, token := range tokens {
		var b strings.Builder
		for j := 0; j < len(token); j++ {
			if token[j] != '~' {
				b.WriteByte(token[j])
				continue
			}
			j++
			if j >= len(token) || (token[j] != '0' && token[j] != '1') {
				return nil, fmt.Errorf("malformed JSON pointer escape")
			}
			if token[j] == '0' {
				b.WriteByte('~')
			} else {
				b.WriteByte('/')
			}
		}
		tokens[i] = b.String()
	}
	return tokens, nil
}

// PatchPathType authorizes reads as well as writes and returns a typed target
// when the path addresses context. Output values are extensible JSON.
func PatchPathType(path string) (reflect.Type, error) {
	tokens, err := PointerTokens(path)
	if err != nil {
		return nil, err
	}
	switch tokens[0] {
	case "sequence", "states":
		if len(tokens) < 3 || strings.TrimSpace(tokens[1]) == "" || tokens[2] != "outputs" {
			return nil, fmt.Errorf("only existing step outputs are editable")
		}
		return nil, nil
	case "context":
		if len(tokens) < 2 {
			return nil, fmt.Errorf("context container is not editable")
		}
		if tokens[1] == "workspace" || tokens[1] == "cell_resolution" {
			return nil, fmt.Errorf("context identity cannot be patched")
		}
		// These are the editable fields represented by the execution context.
		// Submission-only policy, profile and restore fields are runtime metadata.
		switch tokens[1] {
		case "environment", "workflow", "git", "artifacts", "recipe_source":
		default:
			return nil, fmt.Errorf("unsupported editable context field")
		}
		typ := reflect.TypeFor[contextual.JobContext]()
		for _, token := range tokens[1:] {
			for typ.Kind() == reflect.Pointer {
				typ = typ.Elem()
			}
			switch typ.Kind() {
			case reflect.Struct:
				found := false
				for j := 0; j < typ.NumField(); j++ {
					f := typ.Field(j)
					if f.IsExported() && strings.Split(f.Tag.Get("json"), ",")[0] == token {
						typ = f.Type
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("unsupported typed context field")
				}
			case reflect.Map:
				typ = typ.Elem()
			case reflect.Slice, reflect.Array:
				if typ.Elem().Kind() == reflect.Uint8 {
					return nil, nil
				}
				typ = typ.Elem()
			case reflect.Interface:
				return nil, nil
			default:
				return nil, fmt.Errorf("cannot descend into typed scalar")
			}
		}
		return typ, nil
	default:
		return nil, fmt.Errorf("editable roots are context, sequence and states")
	}
}

// ValidatePatchValue checks unknown typed fields even for custom unmarshallers.
func ValidatePatchValue(raw json.RawMessage, typ reflect.Type) error {
	if err := validateTypedJSON(raw, typ, "context"); err != nil {
		return err
	}
	dst := reflect.New(typ).Interface()
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid typed context value")
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
