package skillinstall

import (
	"fmt"
	"io/fs"
	"path"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
)

type SourcesFile struct {
	Version int                      `yaml:"version"`
	Sources map[string]TrustedSource `yaml:"sources"`
}

type TrustedSource struct {
	Type             string   `yaml:"type"`
	Owner            string   `yaml:"owner"`
	Repo             string   `yaml:"repo"`
	AllowedRefs      []string `yaml:"allowed_refs"`
	AllowedPaths     []string `yaml:"allowed_paths"`
	SelectorPrefixes []string `yaml:"selector_prefixes"`
	InstallByDefault bool     `yaml:"install_by_default"`
	AllowScripts     bool     `yaml:"allow_scripts"`
}

func LoadSources(fsys fs.FS) (SourcesFile, error) {
	raw, err := fs.ReadFile(fsys, "sources.yaml")
	if err != nil {
		return SourcesFile{}, fmt.Errorf("read sources.yaml: %w", err)
	}
	var sources SourcesFile
	if err := yamlv3.Unmarshal(raw, &sources); err != nil {
		return SourcesFile{}, fmt.Errorf("parse sources.yaml: %w", err)
	}
	if err := sources.Validate(); err != nil {
		return SourcesFile{}, err
	}
	return sources, nil
}

func (s SourcesFile) Validate() error {
	if s.Version != 1 {
		return fmt.Errorf("unsupported sources.yaml version %d", s.Version)
	}
	if len(s.Sources) == 0 {
		return fmt.Errorf("sources.yaml must define at least one source")
	}
	for name, source := range s.Sources {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("source name is required")
		}
		if source.Type != "github" {
			return fmt.Errorf("source %s: unsupported type %q", name, source.Type)
		}
		if strings.TrimSpace(source.Owner) == "" || strings.TrimSpace(source.Repo) == "" {
			return fmt.Errorf("source %s: owner and repo are required", name)
		}
		if len(source.AllowedRefs) == 0 {
			return fmt.Errorf("source %s: allowed_refs is required", name)
		}
		if len(source.AllowedPaths) == 0 {
			return fmt.Errorf("source %s: allowed_paths is required", name)
		}
	}
	return nil
}

func (s SourcesFile) TrustedGitHubSkill(owner string, repo string, ref string, skillPath string) (string, bool, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	ref = strings.TrimSpace(ref)
	skillPath = strings.TrimPrefix(path.Clean(strings.TrimSpace(skillPath)), "/")
	for name, source := range s.Sources {
		if source.Type != "github" || source.Owner != owner || source.Repo != repo {
			continue
		}
		if !globAny(source.AllowedRefs, ref) {
			return name, false, fmt.Errorf("source %s: ref %q is not allowed", name, ref)
		}
		if !globAny(source.AllowedPaths, skillPath) {
			return name, false, fmt.Errorf("source %s: path %q is not allowed", name, skillPath)
		}
		return name, true, nil
	}
	return "", false, nil
}

func (s SourcesFile) TrustedSelector(selector string) (string, bool, error) {
	selector = strings.TrimSpace(selector)
	for name, source := range s.Sources {
		for _, prefix := range source.SelectorPrefixes {
			if !strings.HasPrefix(selector, prefix) {
				continue
			}
			ref := selectorRef(strings.TrimPrefix(selector, prefix))
			if ref == "" {
				return name, false, fmt.Errorf("selector %q is missing a ref", selector)
			}
			if !globAny(source.AllowedRefs, ref) {
				return name, false, fmt.Errorf("selector %q uses untrusted ref %q", selector, ref)
			}
			return name, true, nil
		}
	}
	return "", false, nil
}

func selectorRef(pathAndRef string) string {
	idx := strings.LastIndex(pathAndRef, "@")
	if idx < 0 || idx == len(pathAndRef)-1 {
		return ""
	}
	return pathAndRef[idx+1:]
}

func globAny(patterns []string, value string) bool {
	for _, pattern := range patterns {
		ok, err := path.Match(pattern, value)
		if err == nil && ok {
			return true
		}
		if err != nil && pattern == value {
			return true
		}
	}
	return false
}
