package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/pkg/execution"
	"gopkg.in/yaml.v3"
)

// PackageList uses the same literal references as recipe execution.packages.
// Project declarations must be portable: installation does not run in the repo.
type PackageList []string

func (p *PackageList) UnmarshalYAML(n *yaml.Node) error {
	*p = nil
	if n.Tag == "!!null" {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("packages: expected a list of strings")
	}
	seen := map[string]bool{}
	for i, item := range n.Content {
		if item.Kind != yaml.ScalarNode || item.Tag != "!!str" {
			return fmt.Errorf("packages[%d]: expected a string", i)
		}
		ref := item.Value
		if err := validateRepositoryPackage(ref); err != nil {
			return fmt.Errorf("packages[%d]: %w", i, err)
		}
		if !seen[ref] {
			*p = append(*p, ref)
			seen[ref] = true
		}
	}
	return nil
}

func validateRepositoryPackage(ref string) error {
	manager, spec, err := execution.ParsePackage(ref)
	if err != nil {
		return err
	}
	localLocation := func(value string) bool {
		value = strings.TrimSpace(value)
		for _, prefix := range []string{".", "/", "~", "file:", "link:", "workspace:", "path:", "git+file:"} {
			if strings.HasPrefix(value, prefix) {
				return true
			}
		}
		return false
	}
	local := localLocation(spec)
	if manager == "nix" {
		local = local || strings.HasPrefix(spec, "#")
	}
	if manager == "uv" {
		if _, location, ok := strings.Cut(spec, "@"); ok {
			local = local || localLocation(location)
		}
	}
	if manager == "pnpm" {
		if at := strings.LastIndex(spec, "@"); at > 0 {
			local = local || localLocation(spec[at+1:])
		}
	}
	if local {
		return fmt.Errorf("package %q must use a portable registry or remote reference, not a local path", ref)
	}
	return nil
}

func (c *ProjectConfig) Packages() []string { return append([]string(nil), c.raw.Packages...) }

// PackageSource is provenance, not a resolved package lock or a full config.
type PackageSource struct {
	Path     string   `json:"path"`
	Digest   string   `json:"digest,omitempty"`
	Missing  bool     `json:"missing,omitempty"`
	Packages []string `json:"packages,omitempty"`
}

// LoadWorkspacePackages reads only the exact restored workspace's config. It
// never searches ancestors, auto-detects a project, or resolves config commands.
func LoadWorkspacePackages(workspace string) (source PackageSource, err error) {
	source.Path = filepath.Join(configDirName, configFileName)
	if workspace == "" {
		return source, fmt.Errorf("workspace root is required")
	}
	for _, relative := range []string{configDirName, source.Path} {
		info, e := os.Lstat(filepath.Join(workspace, relative))
		if os.IsNotExist(e) {
			source.Missing = true
			return source, nil
		}
		if e != nil {
			return source, fmt.Errorf("%s: %w", source.Path, e)
		}
		if info.Mode()&os.ModeSymlink != 0 || (relative == configDirName && !info.IsDir()) || (relative == source.Path && !info.Mode().IsRegular()) {
			return source, fmt.Errorf("%s: expected a regular config file in a real .c2 directory (symlinks are not supported)", source.Path)
		}
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return source, err
	}
	defer root.Close()
	raw, err := root.ReadFile(source.Path)
	if err != nil {
		return source, fmt.Errorf("%s: %w", source.Path, err)
	}
	source.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	var config struct {
		Packages PackageList `yaml:"packages"`
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return source, fmt.Errorf("%s: %w", source.Path, err)
	}
	source.Packages = append([]string(nil), config.Packages...)
	return source, nil
}
