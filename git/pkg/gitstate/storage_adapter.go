package gitstate

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

const thinPackSubdir = "git/thin-packs"

// StorageAdapter provides filesystem-agnostic primitives for thin-pack management.
type StorageAdapter interface {
	EnsureLocation(ctx context.Context, baseURI string) (string, error)
	PutBlob(ctx context.Context, baseURI, relativePath, sourcePath string) (string, error)
	ResolvePath(ctx context.Context, baseURI, relativePath string) (string, error)
	ListBlobs(ctx context.Context, baseURI, prefix string) ([]string, error)
}

// FileAdapter implements StorageAdapter for file:// URIs.
type FileAdapter struct{}

// EnsureLocation ensures the base directory exists and returns its local filesystem path.
func (FileAdapter) EnsureLocation(_ context.Context, baseURI string) (string, error) {
	path, err := fileURIToPath(baseURI)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("create base path: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(path, thinPackSubdir), 0o755); err != nil {
		return "", fmt.Errorf("create thin-pack path: %w", err)
	}
	return path, nil
}

// PutBlob for the file adapter moves/copies the blob into the target location and returns the relative path.
func (FileAdapter) PutBlob(_ context.Context, baseURI, relativePath, sourcePath string) (string, error) {
	basePath, err := fileURIToPath(baseURI)
	if err != nil {
		return "", err
	}
	destination := filepath.Join(basePath, relativePath)
	if filepath.Clean(sourcePath) == filepath.Clean(destination) {
		return relativePath, nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return "", fmt.Errorf("prepare destination: %w", err)
	}
	if err := copyFile(sourcePath, destination); err != nil {
		return "", err
	}
	return relativePath, nil
}

// ResolvePath returns the absolute path for the provided relative blob path.
func (FileAdapter) ResolvePath(_ context.Context, baseURI, relativePath string) (string, error) {
	basePath, err := fileURIToPath(baseURI)
	if err != nil {
		return "", err
	}
	return filepath.Join(basePath, relativePath), nil
}

// ListBlobs lists blobs beneath the base URI using the supplied prefix relative to the base path.
func (FileAdapter) ListBlobs(_ context.Context, baseURI, prefix string) ([]string, error) {
	basePath, err := fileURIToPath(baseURI)
	if err != nil {
		return nil, err
	}
	pattern := filepath.Join(basePath, prefix)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("list blobs: %w", err)
	}
	results := make([]string, 0, len(matches))
	for _, match := range matches {
		rel, err := filepath.Rel(basePath, match)
		if err != nil {
			return nil, fmt.Errorf("compute relative path: %w", err)
		}
		results = append(results, rel)
	}
	return results, nil
}

func fileURIToPath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("blobstore URI is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse blobstore URI: %w", err)
	}
	if u.Scheme == "" || u.Scheme == "file" {
		path := u.Path
		if path == "" {
			path = u.Host
		}
		if path == "" {
			return "", fmt.Errorf("file URI must include a path")
		}
		return filepath.Clean(path), nil
	}
	return "", fmt.Errorf("unsupported storage scheme: %s", u.Scheme)
}

func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer source.Close()

	dest, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer dest.Close()

	if _, err := io.Copy(dest, source); err != nil {
		return fmt.Errorf("copy blob: %w", err)
	}
	return nil
}
