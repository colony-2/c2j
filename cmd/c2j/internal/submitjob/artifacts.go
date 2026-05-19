package submitjob

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/swf-go/pkg/swf"
)

type submitArtifactSpec struct {
	Name       string
	SourcePath string
}

func loadSubmitArtifacts(opts Options, recipeName string, embeddedRecipe bool) ([]swf.Artifact, error) {
	if len(opts.ArtifactSpecs) == 0 {
		return nil, nil
	}

	seen := make(map[string]string, len(opts.ArtifactSpecs))
	artifacts := make([]swf.Artifact, 0, len(opts.ArtifactSpecs))
	reservedRecipeArtifactName := recipeName + starter.RecipeArtifactSuffix

	for _, rawSpec := range opts.ArtifactSpecs {
		parsed, err := parseSubmitArtifactSpec(rawSpec)
		if err != nil {
			return nil, err
		}
		if err := validateSubmitArtifactName(parsed.Name); err != nil {
			return nil, fmt.Errorf("--artifact %q has invalid artifact name %q: %w", rawSpec, parsed.Name, err)
		}
		if embeddedRecipe && parsed.Name == reservedRecipeArtifactName {
			return nil, fmt.Errorf("--artifact %q conflicts with internal recipe artifact %q", rawSpec, reservedRecipeArtifactName)
		}
		if previous, exists := seen[parsed.Name]; exists {
			return nil, fmt.Errorf("--artifact %q duplicates artifact name %q already used by %q", rawSpec, parsed.Name, previous)
		}
		seen[parsed.Name] = rawSpec

		sourcePath, err := absPathFromWorkingDir(opts.WorkingDir, parsed.SourcePath)
		if err != nil {
			return nil, fmt.Errorf("resolve --artifact %q: %w", rawSpec, err)
		}
		info, err := os.Stat(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("stat --artifact %q source: %w", rawSpec, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("--artifact %q source must be a regular file", rawSpec)
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("read --artifact %q source: %w", rawSpec, err)
		}
		artifacts = append(artifacts, swf.NewArtifactFromBytes(parsed.Name, data))
	}

	return artifacts, nil
}

func parseSubmitArtifactSpec(rawSpec string) (submitArtifactSpec, error) {
	spec := strings.TrimSpace(rawSpec)
	if spec == "" {
		return submitArtifactSpec{}, fmt.Errorf("--artifact cannot be empty")
	}

	name := ""
	sourcePath := spec
	if before, after, found := strings.Cut(spec, "="); found {
		name = strings.TrimSpace(before)
		sourcePath = strings.TrimSpace(after)
	} else {
		name = filepath.Base(sourcePath)
	}

	if name == "" {
		return submitArtifactSpec{}, fmt.Errorf("--artifact %q is missing an artifact name", rawSpec)
	}
	if sourcePath == "" {
		return submitArtifactSpec{}, fmt.Errorf("--artifact %q is missing a source path", rawSpec)
	}
	return submitArtifactSpec{Name: name, SourcePath: sourcePath}, nil
}

func validateSubmitArtifactName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("name must use slash-separated paths")
	}
	if strings.Contains(name, "=") {
		return fmt.Errorf("name cannot contain =")
	}
	if path.IsAbs(name) || hasWindowsDrivePrefix(name) {
		return fmt.Errorf("name must be relative")
	}

	parts := strings.Split(name, "/")
	for _, part := range parts {
		switch part {
		case "":
			return fmt.Errorf("name cannot contain empty path segments")
		case ".":
			return fmt.Errorf("name cannot contain . path segments")
		case "..":
			return fmt.Errorf("name cannot contain .. path segments")
		}
	}
	if path.Clean(name) != name {
		return fmt.Errorf("name must be normalized")
	}
	return nil
}

func hasWindowsDrivePrefix(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	letter := name[0]
	return (letter >= 'A' && letter <= 'Z') || (letter >= 'a' && letter <= 'z')
}
