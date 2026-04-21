package extensions

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	invschema "github.com/invopop/jsonschema"
	jsonschemav6 "github.com/santhosh-tekuri/jsonschema/v6"
	yaml "gopkg.in/yaml.v3"
)

// ExtensionOpSpec models the op.yaml configuration for a selector-resolved extension op.
type ExtensionOpSpec struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Version      string            `yaml:"version"`
	Shell        string            `yaml:"shell"`
	Run          string            `yaml:"run"`
	Command      []string          `yaml:"command"`
	Env          map[string]string `yaml:"env"`
	Timeout      string            `yaml:"timeout"`
	InputSchema  map[string]any    `yaml:"input_schema"`
	OutputSchema map[string]any    `yaml:"output_schema"`
}

func validateExtensionOpManifest(raw []byte) error {
	var top map[string]any
	if err := yaml.Unmarshal(raw, &top); err != nil {
		return err
	}
	if _, ok := top["args"]; ok {
		return fmt.Errorf("manifest field %q is not supported; use %q with the full argv instead", "args", "command")
	}
	if _, ok := top["working_directory"]; ok {
		return fmt.Errorf("manifest field %q is not supported; working directory is engine-controlled", "working_directory")
	}
	return nil
}

func parseDurationOrZero(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}

// parseSchema converts a YAML-parsed schema (map[string]any) into both an
// invopop schema (for documentation) and a compiled jsonschema/v6 validator.
func parseSchema(m map[string]any) (*invschema.Schema, *jsonschemav6.Schema, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal schema: %w", err)
	}

	var doc invschema.Schema
	if err := json.Unmarshal(b, &doc); err != nil {
		doc = invschema.Schema{}
	}
	if rv, ok := m["required"]; ok {
		switch rr := rv.(type) {
		case []any:
			var req []string
			for _, it := range rr {
				if s, ok := it.(string); ok {
					req = append(req, s)
				}
			}
			if len(req) > 0 {
				doc.Required = req
			}
		case []string:
			if len(rr) > 0 {
				doc.Required = rr
			}
		}
	}

	comp := jsonschemav6.NewCompiler()
	comp.DefaultDraft(jsonschemav6.Draft2020)
	var docValue any
	if err := json.Unmarshal(b, &docValue); err != nil {
		return &doc, nil, fmt.Errorf("decode schema: %w", err)
	}
	if err := comp.AddResource("inmem://op-schema.json", docValue); err != nil {
		return &doc, nil, fmt.Errorf("add resource: %w", err)
	}
	compiled, err := comp.Compile("inmem://op-schema.json")
	if err != nil {
		return &doc, nil, fmt.Errorf("compile schema: %w", err)
	}
	return &doc, compiled, nil
}
