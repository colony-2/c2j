package recipe

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/colony-2/c2j/pkg/gitprofile"
	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// GitProfileSpec is a scalar property; a nonnil spec with nil Value means clear.
type GitProfileSpec struct{ Value any }

func (s GitProfileSpec) MarshalYAML() (any, error)    { return s.Value, nil }
func (s GitProfileSpec) MarshalJSON() ([]byte, error) { return json.Marshal(s.Value) }
func (s *GitProfileSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Tag == "!!null" {
		s.Value = nil
		return nil
	}
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return fmt.Errorf("git_profile must be a name, template, or null")
	}
	if !isTemplateString(n.Value) {
		if _, err := gitprofile.Key(n.Value); err != nil {
			return err
		}
	}
	s.Value = n.Value
	return nil
}
func (GitProfileSpec) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{{Type: "string"}, {Type: "null"}}}
}

// yaml.v3 does not invoke field unmarshallers for null. Preserve its presence
// after decoding the concrete node, including promoted root/state metadata.
func decodeGitProfile(n *yaml.Node, impl any) error {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "git_profile" {
			var spec GitProfileSpec
			if err := spec.UnmarshalYAML(n.Content[i+1]); err != nil {
				return err
			}
			field := reflect.ValueOf(impl).Elem().FieldByName("GitProfile")
			if !field.IsValid() || !field.CanSet() {
				return fmt.Errorf("git_profile belongs on the shared definition or enclosing node")
			}
			field.Set(reflect.ValueOf(&spec))
		}
	}
	return nil
}
