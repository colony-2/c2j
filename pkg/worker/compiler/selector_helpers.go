package compiler

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/internal/repository"
)

const (
	CellRecipeDirectory = ".c2j/recipes"
	DefaultRecipeName   = "default"
	DefaultRecipeRef    = "main"
)

func IsGitRecipeSelector(selector string) bool {
	return isGitRecipeSelector(selector)
}

func IsLocalRecipeFileReference(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || isGitRecipeSelector(value) {
		return false
	}

	switch {
	case filepath.IsAbs(value):
		return true
	case value == "." || value == "..":
		return true
	case strings.HasPrefix(value, "./"), strings.HasPrefix(value, "../"):
		return true
	case strings.Contains(value, "/"), strings.Contains(value, "\\"):
		return true
	}

	switch strings.ToLower(filepath.Ext(value)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func IsCellRecipeName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || isGitRecipeSelector(value) {
		return false
	}
	return !IsLocalRecipeFileReference(value)
}

func NormalizeGitRepositorySource(source string) (string, error) {
	return repository.Normalize(source)
}

func BuildCellRecipeSelector(repositorySource string, recipeName string, ref string) (string, error) {
	repoSource, err := NormalizeGitRepositorySource(repositorySource)
	if err != nil {
		return "", err
	}

	recipeName, err = validateCellRecipeName(recipeName)
	if err != nil {
		return "", err
	}

	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = DefaultRecipeRef
	}

	recipePath := path.Join(CellRecipeDirectory, recipeName+".yaml")
	return fmt.Sprintf("git+%s//%s@%s", repoSource, recipePath, ref), nil
}

func RepositoryNameFromSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}

	if strings.HasPrefix(source, "git@") {
		if idx := strings.Index(source, ":"); idx >= 0 && idx < len(source)-1 {
			return trimGitRepositorySuffix(path.Base(source[idx+1:]))
		}
	}

	if parsed, err := url.Parse(source); err == nil && parsed.Scheme != "" {
		return trimGitRepositorySuffix(path.Base(strings.TrimSuffix(parsed.Path, "/")))
	}

	return trimGitRepositorySuffix(path.Base(source))
}

func validateCellRecipeName(recipeName string) (string, error) {
	recipeName = strings.TrimSpace(recipeName)
	switch {
	case recipeName == "":
		return "", fmt.Errorf("recipe name is required")
	case recipeName == "." || recipeName == "..":
		return "", fmt.Errorf("recipe name %q is invalid", recipeName)
	case strings.Contains(recipeName, "/"), strings.Contains(recipeName, "\\"):
		return "", fmt.Errorf("recipe name %q must not contain path separators", recipeName)
	default:
		return recipeName, nil
	}
}

func trimGitRepositorySuffix(name string) string {
	return strings.TrimSuffix(name, ".git")
}
