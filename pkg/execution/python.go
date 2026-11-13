package execution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Python describes a complete script environment. Python dependency semantics
// belong to uv; c2j validates only the declaration's shape and portability.
type Python struct {
	RequiresPython string   `json:"requires-python,omitempty" yaml:"requires-python,omitempty"`
	Dependencies   []string `json:"dependencies" yaml:"dependencies"`
}

func ChoosePython(base, override *Python) *Python {
	if override != nil {
		base = override
	}
	if base == nil {
		return nil
	}
	return &Python{RequiresPython: base.RequiresPython, Dependencies: append([]string{}, base.Dependencies...)}
}

func (p *Python) Validate() error {
	if p == nil {
		return nil
	}
	for _, s := range append([]string{p.RequiresPython}, p.Dependencies...) {
		if strings.Contains(s, "{{") {
			return fmt.Errorf("python requirements must be literal strings")
		}
		if strings.ContainsRune(s, 0) {
			return fmt.Errorf("python requirements must not contain NUL")
		}
	}
	// Local sources cannot identify software shared across unrelated workspaces.
	for _, dep := range p.Dependencies {
		if _, location, ok := strings.Cut(dep, "@"); ok {
			location = strings.TrimSpace(location)
			for _, prefix := range []string{"file:", "git+file:", ".", "/", "~"} {
				if strings.HasPrefix(location, prefix) {
					return fmt.Errorf("python dependency must use a portable registry or remote reference: %q", dep)
				}
			}
		}
	}
	return nil
}

func ValidatePythonYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("python: expected an object")
	}
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		name, value := n.Content[i].Value, n.Content[i+1]
		if seen[name] {
			return fmt.Errorf("python: duplicate field %q", name)
		}
		seen[name] = true
		switch name {
		case "requires-python":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return fmt.Errorf("python.requires-python: expected a string")
			}
		case "dependencies":
			if value.Kind != yaml.SequenceNode {
				return fmt.Errorf("python.dependencies: expected a list of strings")
			}
			for _, item := range value.Content {
				if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
					return fmt.Errorf("python.dependencies: expected a list of strings")
				}
			}
		default:
			return fmt.Errorf("python: unknown field %q", name)
		}
	}
	return nil
}

func (p *Python) UnmarshalYAML(n *yaml.Node) error {
	if err := ValidatePythonYAML(n); err != nil {
		return err
	}
	type plain Python
	var v plain
	if err := n.Decode(&v); err != nil {
		return err
	}
	*p = *ChoosePython(nil, (*Python)(&v))
	return p.Validate()
}

// Preserve an explicitly empty environment through durable Go/JSON requests.
func (p Python) MarshalJSON() ([]byte, error) {
	type plain Python
	return json.Marshal(plain(*ChoosePython(nil, &p)))
}

func (p *Python) UnmarshalJSON(raw []byte) error {
	// The YAML node check also rejects JSON nulls and non-string list elements.
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return err
	}
	if len(node.Content) != 1 {
		return fmt.Errorf("python: expected an object")
	}
	if err := ValidatePythonYAML(node.Content[0]); err != nil {
		return err
	}
	type plain Python
	var v plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&v); err != nil {
		return err
	}
	*p = *ChoosePython(nil, (*Python)(&v))
	return p.Validate()
}

// ValidatePythonField checks a containing YAML/JSON object before pointer decoding.
func ValidatePythonField(raw []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	n := doc.Content[0]
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == "python" {
			return ValidatePythonYAML(n.Content[i+1])
		}
	}
	return nil
}
