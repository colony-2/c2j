package compiler

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
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
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("git repository source is required")
	}

	if isLikelyLocalPath(source) {
		absPath, err := filepath.Abs(source)
		if err != nil {
			return "", fmt.Errorf("resolve local repository path %q: %w", source, err)
		}
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String(), nil
	}

	if strings.HasPrefix(source, "git@") {
		return normalizeSCPGitRemote(source)
	}

	parsed, err := url.Parse(source)
	if err == nil && parsed.Scheme != "" {
		switch parsed.Scheme {
		case "file":
			if parsed.Path == "" {
				return "", fmt.Errorf("file repository source %q has an empty path", source)
			}
			absPath, absErr := filepath.Abs(parsed.Path)
			if absErr != nil {
				return "", fmt.Errorf("resolve file repository source %q: %w", source, absErr)
			}
			return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String(), nil
		case "http", "https", "ssh":
			return source, nil
		default:
			return "", fmt.Errorf("unsupported git repository scheme %q", parsed.Scheme)
		}
	}

	if looksLikeCanonicalRepositoryRef(source) {
		trimmed := strings.TrimSuffix(source, ".git")
		return "https://" + trimmed + ".git", nil
	}

	return "", fmt.Errorf("unsupported git repository source %q", source)
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

func isLikelyLocalPath(source string) bool {
	switch {
	case strings.HasPrefix(source, "/"):
		return true
	case source == "." || source == "..":
		return true
	case strings.HasPrefix(source, "./"), strings.HasPrefix(source, "../"):
		return true
	}

	if _, err := os.Stat(source); err == nil {
		return true
	}
	return false
}

func looksLikeCanonicalRepositoryRef(source string) bool {
	source = strings.TrimSpace(source)
	parts := strings.Split(source, "/")
	if len(parts) < 3 {
		return false
	}
	return strings.Contains(parts[0], ".")
}

func normalizeSCPGitRemote(source string) (string, error) {
	colonIdx := strings.Index(source, ":")
	if colonIdx <= 0 || colonIdx >= len(source)-1 {
		return "", fmt.Errorf("unsupported git repository source %q", source)
	}

	host := source[:colonIdx]
	repoPath := path.Clean(source[colonIdx+1:])
	if repoPath == "." || repoPath == ".." || strings.HasPrefix(repoPath, "../") {
		return "", fmt.Errorf("unsupported git repository source %q", source)
	}
	return "ssh://" + host + "/" + repoPath, nil
}

func trimGitRepositorySuffix(name string) string {
	return strings.TrimSuffix(name, ".git")
}
