package thinpackrebase

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/invopop/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

var fullHash = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// Keep explicit null/incomplete resolution invalid for typed JSON callers too.
func (input *ThinpackRebaseInput) UnmarshalJSON(data []byte) error {
	var declared map[string]any
	if err := json.Unmarshal(data, &declared); err != nil {
		return err
	}
	if err := input.ValidateDeclaredOpInput(declared, false); err != nil {
		return err
	}
	type plain ThinpackRebaseInput
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*input = ThinpackRebaseInput(decoded)
	return input.ValidateOpInput()
}

// ValidateDeclaredOpInput runs before compiler defaults and again on the worker.
// Templates may bind confirmations, but restoration-time defaults are not them.
func (*ThinpackRebaseInput) ValidateDeclaredOpInput(input map[string]any, allowNulls bool) error {
	raw, present := input["resolution"]
	if !present {
		return nil
	}
	if raw == nil && allowNulls {
		return nil // unresolved template during static validation
	}
	resolution, ok := raw.(map[string]any)
	// A whole-object template is still explicit. Its concrete shape and tree
	// are checked after resolution; the other confirmations cannot default.
	templateResolution := false
	if s, isString := raw.(string); isString && strings.Contains(s, "{{") {
		templateResolution = true
	}
	if !ok && !templateResolution {
		return fmt.Errorf("resolution must be an object containing tree_hash")
	}
	for name := range resolution {
		if name != "tree_hash" {
			return fmt.Errorf("unknown resolution field %q", name)
		}
	}
	for _, field := range []string{"base_hash", "persist_hash", "target_base_hash", "resolution.tree_hash"} {
		if field == "resolution.tree_hash" && templateResolution {
			continue
		}
		value, exists := input[field]
		if field == "resolution.tree_hash" {
			value, exists = resolution["tree_hash"]
		}
		if !exists {
			return fmt.Errorf("resolved-tree adoption requires explicit %s", field)
		}
		if value == nil && allowNulls {
			continue
		}
		s, ok := value.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return fmt.Errorf("resolved-tree adoption requires explicit %s", field)
		}
		if strings.Contains(s, contextual.GitExecutionBaseSentinel) || strings.Contains(s, contextual.GitExecutionHeadSentinel) {
			return fmt.Errorf("%s must confirm a hash, not a task-time Git default", field)
		}
	}
	return nil
}

func (input *ThinpackRebaseInput) ValidateOpInput() error {
	if input.Resolution == nil {
		return nil
	}
	for _, field := range []struct{ name, hash string }{
		{"base_hash", input.BaseHash}, {"persist_hash", input.PersistHash},
		{"target_base_hash", input.TargetBaseHash}, {"resolution.tree_hash", input.Resolution.TreeHash},
	} {
		if !fullHash.MatchString(field.hash) {
			return fmt.Errorf("resolved-tree adoption requires a full 40-character hexadecimal %s", field.name)
		}
	}
	return nil
}

// JSONSchemaExtend keeps discovery schemas consistent with conditional runtime
// validation. Replay retains its existing defaults and abbreviated hash support.
func (ThinpackRebaseInput) JSONSchemaExtend(schema *jsonschema.Schema) {
	props := orderedmap.New[string, *jsonschema.Schema]()
	for _, field := range []string{"base_hash", "persist_hash", "target_base_hash"} {
		props.Set(field, &jsonschema.Schema{Type: "string", Pattern: `^[0-9a-fA-F]{40}$`})
	}
	schema.If = &jsonschema.Schema{Required: []string{"resolution"}}
	schema.Then = &jsonschema.Schema{
		Required: []string{"base_hash", "persist_hash", "target_base_hash"}, Properties: props,
	}
}
