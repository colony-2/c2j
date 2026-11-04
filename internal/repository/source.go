// Package repository contains shared repository identity normalization.
package repository

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
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
	id, err := NormalizeIdentity(source)
	if err != nil {
		return "", err
	}
	// Transport is distinct from the canonical registry identity.
	return "https://" + id + ".git", nil
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

// NormalizeIdentity uses JobDB's portable repository identity contract.
func NormalizeIdentity(source string) (string, error) { return jobdb.NormalizeCellRepo(source) }
