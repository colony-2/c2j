package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DependentEdit is a validated config edit. Preparing it never writes files.
type DependentEdit struct {
	Path    string
	Repo    string
	Status  string
	YAML    []byte
	Changed bool

	original []byte
	info     os.FileInfo
}

// PrepareDependentEdit adds or updates a manual entry. A nil description leaves
// existing metadata alone; a pointer to an empty string clears it.
func PrepareDependentEdit(ctx context.Context, startDir, value string, description *string) (*DependentEdit, error) {
	path, err := DiscoverConfigPath(startDir)
	if errors.Is(err, ErrConfigNotFound) {
		return nil, fmt.Errorf("no project config found; run c2j init --no-skills at the project root first")
	}
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config %s must be a regular file, not a symlink", path)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := editableConfigDocument(original)
	if err != nil {
		return nil, fmt.Errorf("edit %s: %w", path, err)
	}
	cfg, err := parseProjectConfig(path, original)
	if err != nil {
		return nil, err
	}
	repo, err := cfg.ExpandCellName(ctx, value)
	if err != nil {
		return nil, err
	}
	edit := &DependentEdit{Path: path, Repo: repo, Status: "unchanged", YAML: original, original: original, info: info}
	root := doc.Content[0]
	deps := mappingValue(root, "dependents")
	if cfg.raw.Dependents.mode != "list" {
		if cfg.raw.Dependents.Command != "" || cfg.raw.Base == "go" {
			entries, err := cfg.AllowedDependentEntries(ctx)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if repoValuesEqual(repo, entry.Repo) && (description == nil || *description == "") {
					edit.Repo = entry.Repo
					edit.Status = "already detected"
					return edit, nil
				}
			}
			return nil, fmt.Errorf("cell add edits manual lists; %s uses discovery: use c2j cells to inspect detected cells; adding undetected cells or descriptions requires a manually maintained dependents list", path)
		}
		if deps != nil {
			return nil, fmt.Errorf("cell add requires dependents in sequence form; edit %s to choose a manual list instead of the command/filter object", path)
		}
	}
	if deps == nil {
		deps = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, stringNode("dependents"), deps)
	}
	var match *yaml.Node
	var locations []string
	for i, node := range deps.Content {
		entry, err := decodeDependentEntry(node)
		if err != nil {
			return nil, err
		}
		if repoValuesEqual(repo, entry.Repo) {
			match = node
			edit.Repo = entry.Repo
			locations = append(locations, fmt.Sprintf("dependents[%d] (line %d)", i, node.Line))
		}
	}
	if len(locations) > 1 {
		return nil, fmt.Errorf("multiple entries match %q: %s; remove the ambiguity before editing", repo, strings.Join(locations, ", "))
	}
	if match == nil {
		match = stringNode(repo)
		deps.Content = append(deps.Content, match)
		edit.Status = "added"
	} else {
		entry, _ := decodeDependentEntry(match)
		if description == nil || entry.Description == *description {
			return edit, nil
		}
		edit.Status = "updated"
	}
	if description != nil && *description != "" {
		if match.Kind == yaml.ScalarNode {
			repoNode := *match
			*match = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: repoNode.HeadComment, FootComment: repoNode.FootComment}
			repoNode.HeadComment, repoNode.FootComment = "", ""
			match.Content = []*yaml.Node{stringNode("repo"), &repoNode}
		}
		desc := mappingValue(match, "description")
		if desc == nil {
			desc = stringNode(*description)
			match.Content = append(match.Content, stringNode("description"), desc)
		} else {
			desc.Value = *description
		}
		if strings.Contains(*description, "\n") {
			desc.Style = yaml.LiteralStyle
		}
	} else if description != nil && match.Kind == yaml.MappingNode {
		for i := 0; i < len(match.Content); i += 2 {
			if match.Content[i].Value == "description" {
				// Keep comments on the removed field as comments on the entry.
				for _, node := range match.Content[i : i+2] {
					for _, comment := range []string{node.HeadComment, node.LineComment, node.FootComment} {
						if comment != "" {
							match.FootComment = strings.TrimSpace(match.FootComment + "\n" + comment)
						}
					}
				}
				match.Content = append(match.Content[:i], match.Content[i+2:]...)
				break
			}
		}
	}
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	if _, err := parseProjectConfig(path, buf.Bytes()); err != nil {
		return nil, err
	}
	edit.YAML = buf.Bytes()
	edit.Changed = true
	return edit, nil
}

// Write atomically replaces the config, rejecting detected intervening changes.
// The last check is optimistic concurrency detection, not cross-process locking.
func (e *DependentEdit) Write() error {
	if !e.Changed {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.Path), ".config-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(e.YAML); err != nil {
		return err
	}
	if err := tmp.Chmod(e.info.Mode().Perm()); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	info, err := os.Lstat(e.Path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(e.info, info) || info.Mode() != e.info.Mode() || !info.ModTime().Equal(e.info.ModTime()) {
		return fmt.Errorf("config %s changed while preparing the edit; retry", e.Path)
	}
	current, err := os.ReadFile(e.Path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, e.original) {
		return fmt.Errorf("config %s changed while preparing the edit; retry", e.Path)
	}
	return os.Rename(tmp.Name(), e.Path)
}

func editableConfigDocument(data []byte) (*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil && err != io.EOF {
		return nil, err
	}
	var next yaml.Node
	if err := decoder.Decode(&next); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("multiple YAML documents are not editable; use one config document")
	}
	if len(doc.Content) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
		// yaml.v3 returns EOF without a node for comment-only documents.
		doc.Content[0].HeadComment = strings.TrimSpace(string(data))
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" && root.Value == "" {
		root.Kind, root.Tag = yaml.MappingNode, "!!map"
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config must be a mapping")
	}
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Tag == "!!merge" {
			return nil, fmt.Errorf("config merge keys are not editable; use explicit config fields")
		}
	}
	if deps := mappingValue(root, "dependents"); deps != nil && hasSharedYAML(deps) {
		return nil, fmt.Errorf("dependents contains YAML anchors, aliases, or merge keys; use explicit entries before editing")
	}
	return &doc, nil
}

func hasSharedYAML(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return true
	}
	for _, child := range node.Content {
		if hasSharedYAML(child) {
			return true
		}
	}
	return false
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}
