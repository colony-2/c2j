package config

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// DependentEntry describes a repository in the dependent cell set.
type DependentEntry struct {
	Repo        string `yaml:"repo"`
	Description string `yaml:"description,omitempty"`
}

func decodeDependentEntry(node *yaml.Node) (DependentEntry, error) {
	var entry DependentEntry
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		entry.Repo = strings.TrimSpace(node.Value)
	} else if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			if seen[key.Value] {
				return entry, fmt.Errorf("duplicate field %q", key.Value)
			}
			seen[key.Value] = true
			if key.Value != "repo" && key.Value != "description" {
				return entry, fmt.Errorf("unknown field %q", key.Value)
			}
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return entry, fmt.Errorf("%s must be a string", key.Value)
			}
			switch key.Value {
			case "repo":
				entry.Repo = strings.TrimSpace(value.Value)
			case "description":
				entry.Description = value.Value
			}
		}
	} else {
		return entry, fmt.Errorf("expected a repository string or mapping with repo and optional description")
	}
	if entry.Repo == "" {
		return entry, fmt.Errorf("repo must not be empty")
	}
	if !isRepoValue(entry.Repo) {
		return entry, fmt.Errorf("repo %q is not a repo", entry.Repo)
	}
	return entry, nil
}

func uniqueDependentEntries(entries []DependentEntry) ([]DependentEntry, error) {
	result := make([]DependentEntry, 0, len(entries))
	seen := make(map[string]int)
	for _, entry := range entries {
		if index, ok := seen[entry.Repo]; ok {
			previous := &result[index]
			if previous.Description != "" && entry.Description != "" && previous.Description != entry.Description {
				return nil, fmt.Errorf("dependents: conflicting descriptions for repo %q", entry.Repo)
			}
			if previous.Description == "" {
				previous.Description = entry.Description
			}
			continue
		}
		seen[entry.Repo] = len(result)
		result = append(result, entry)
	}
	return result, nil
}

// DependentEntries resolves descriptions with their repository locations.
func (c *ProjectConfig) DependentEntries(ctx context.Context) ([]DependentEntry, error) {
	if c.raw.Dependents.mode == "list" && c.raw.Dependents.Entries != nil {
		return uniqueDependentEntries(c.raw.Dependents.Entries)
	}
	repos, err := c.dependentSourceRepos(ctx)
	if err != nil {
		return nil, err
	}
	if repos == nil {
		return nil, nil
	}
	entries := make([]DependentEntry, 0, len(repos))
	for _, repo := range repos {
		entries = append(entries, DependentEntry{Repo: repo})
	}
	return entries, nil
}

// AllowedDependentEntries applies discovery filters while retaining metadata.
func (c *ProjectConfig) AllowedDependentEntries(ctx context.Context) ([]DependentEntry, error) {
	entries, err := c.DependentEntries(ctx)
	if err != nil {
		return nil, err
	}
	filter, err := c.dependentFilter(ctx)
	if err != nil {
		return nil, err
	}
	if filter == nil {
		return entries, nil
	}
	filtered := make([]DependentEntry, 0, len(entries))
	for _, entry := range entries {
		if filter.MatchString(entry.Repo) {
			filtered = append(filtered, entry)
		}
	}
	return filtered, nil
}

func entryRepos(entries []DependentEntry) []string {
	if entries == nil {
		return nil
	}
	repos := make([]string, 0, len(entries))
	for _, entry := range entries {
		repos = append(repos, entry.Repo)
	}
	return repos
}
