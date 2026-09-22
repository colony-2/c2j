// Package repository contains shared repository identity normalization.
package repository

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func Normalize(source string) (string, error) {
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

	parsed, err := url.Parse(source)
	if err == nil && parsed.Scheme == "file" {
		if parsed.Path == "" {
			return "", fmt.Errorf("file repository source %q has an empty path", source)
		}
		absPath, absErr := filepath.Abs(parsed.Path)
		if absErr != nil {
			return "", fmt.Errorf("resolve file repository source %q: %w", source, absErr)
		}
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absPath)}).String(), nil
	}
	return NormalizeIdentity(source)
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

// NormalizeIdentity normalizes explicit repository identities without filesystem
// access. Local identities must already be absolute file URLs.
func NormalizeIdentity(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("git repository source is required")
	}
	if source == "." || source == ".." || strings.HasPrefix(source, "/") || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") {
		return "", fmt.Errorf("repository identity must not be a local path")
	}
	if strings.HasPrefix(source, "git@") {
		return normalizeSCPGitRemote(source)
	}

	parsed, err := url.Parse(source)
	if err == nil && parsed.Scheme != "" {
		switch parsed.Scheme {
		case "file":
			if !filepath.IsAbs(parsed.Path) {
				return "", fmt.Errorf("file repository identity must be absolute")
			}
			return (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Clean(parsed.Path))}).String(), nil
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
