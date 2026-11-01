package execution

import (
	"fmt"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

var capabilityName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._/-][a-z0-9]+)*$`)

func validateCapabilities(names []string) error {
	for _, name := range names {
		if !capabilityName.MatchString(name) {
			return fmt.Errorf("execution.capabilities: invalid capability name %q", name)
		}
	}
	return nil
}

func mergeCapabilities(base, patch []string) []string {
	names := mergePackages(base, patch)
	sort.Strings(names)
	return names
}

// ValidateCapabilitiesYAML prevents YAML's implicit scalar-to-string coercion
// at both recipe execution and node execution_needs boundaries.
func ValidateCapabilitiesYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("execution.capabilities must be a string array")
	}
	var names []string
	for _, item := range node.Content {
		if item.Kind == yaml.AliasNode {
			item = item.Alias
		}
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return fmt.Errorf("execution.capabilities must contain only strings")
		}
		names = append(names, item.Value)
	}
	return validateCapabilities(names)
}
