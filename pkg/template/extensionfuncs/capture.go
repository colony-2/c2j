package extensionfuncs

import (
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/colony-2/c2j/internal/pathutil"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/recipe"
)

// CapturePackages saves immutable Git source files, including sibling helpers
// used by imported functions. Recipe replay must not need the source checkout.
func CapturePackages(ctx context.Context, imports []recipe.ExtensionFunctionImport, opts BuildOptions) ([]recipe.FunctionPackageSnapshot, error) {
	var out []recipe.FunctionPackageSnapshot
	seen := map[string]bool{}
	for _, imp := range imports {
		if seen[imp.Selector] {
			continue
		}
		seen[imp.Selector] = true
		pkg, err := loadPackage(ctx, imp.Selector, opts)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(pkg.resolved.ProjectRoot, pkg.resolved.Dir)
		if err != nil {
			return nil, err
		}
		snapshot := recipe.FunctionPackageSnapshot{Selector: imp.Selector, ResolvedSelector: pkg.resolved.ResolvedSelector, ResolvedCommit: pkg.resolved.ResolvedCommit, PackageDir: filepath.ToSlash(relative), Files: map[string]string{}, FileModes: map[string]uint32{}, Symlinks: map[string]string{}}
		err = filepath.WalkDir(pkg.resolved.ProjectRoot, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() == ".c2j-source-cache.json" {
				return nil
			}
			rel, err := filepath.Rel(pkg.resolved.ProjectRoot, filename)
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				target, err := os.Readlink(filename)
				if err != nil {
					return err
				}
				if !safeCaptureLink(filepath.ToSlash(rel), target) {
					return fmt.Errorf("captured CEL symlink escapes source: %s", rel)
				}
				if err := captureLinkInside(pkg.resolved.ProjectRoot, rel); err != nil {
					return err
				}
				snapshot.Symlinks[filepath.ToSlash(rel)] = target
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("captured CEL source must contain ordinary files: %s", filename)
			}
			bytes, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			snapshot.Files[filepath.ToSlash(rel)] = base64.StdEncoding.EncodeToString(bytes)
			info, err := entry.Info()
			if err != nil {
				return err
			}
			snapshot.FileModes[filepath.ToSlash(rel)] = uint32(info.Mode().Perm())
			return nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot)
	}
	return out, nil
}
func capturedPackage(ctx context.Context, selector string, snapshots []recipe.FunctionPackageSnapshot) (*extops.ResolvedSelectorPath, error) {
	if snapshots == nil {
		return nil, nil
	}
	var found *recipe.FunctionPackageSnapshot
	for i := range snapshots {
		if snapshots[i].Selector == selector {
			if found != nil {
				return nil, fmt.Errorf("duplicate captured CEL package %q", selector)
			}
			found = &snapshots[i]
		}
	}
	// A recorded package inventory is authoritative; never refresh a missing entry.
	if found == nil {
		return nil, fmt.Errorf("captured CEL package %q is missing", selector)
	}
	if !safeCapturePath(found.PackageDir, true) {
		return nil, fmt.Errorf("invalid captured CEL package directory")
	}
	dir, err := os.MkdirTemp("", "c2j-captured-cel-*")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	for name, encoded := range found.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !safeCapturePath(name, false) {
			return nil, fmt.Errorf("invalid captured CEL file path %q", name)
		}
		bytes, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("invalid captured CEL file %q: %w", name, err)
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		mode, exists := found.FileModes[name]
		if !exists || mode & ^uint32(0777) != 0 {
			return nil, fmt.Errorf("invalid captured CEL file mode %q", name)
		}
		if err := os.WriteFile(path, bytes, fs.FileMode(mode)); err != nil {
			return nil, err
		}
	}
	// Restore links after ordinary files, so no write can follow an artifact's
	// link. Every target stays within this captured source root.
	for link := range found.Symlinks {
		for file := range found.Files {
			if strings.HasPrefix(file, link+"/") {
				return nil, fmt.Errorf("captured CEL file descends through symlink %q", link)
			}
		}
		for other := range found.Symlinks {
			if strings.HasPrefix(other, link+"/") {
				return nil, fmt.Errorf("captured CEL symlink descends through symlink %q", link)
			}
		}
	}
	for name, target := range found.Symlinks {
		if !safeCaptureLink(name, target) {
			return nil, fmt.Errorf("invalid captured CEL symlink %q", name)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		if err := os.Symlink(target, path); err != nil {
			return nil, err
		}
	}
	for name := range found.Symlinks {
		if err := captureLinkInside(dir, name); err != nil {
			return nil, err
		}
	}
	context.AfterFunc(ctx, func() { _ = os.RemoveAll(dir) })
	ok = true
	return &extops.ResolvedSelectorPath{Selector: selector, ResolvedSelector: found.ResolvedSelector, ResolvedCommit: found.ResolvedCommit, ProjectRoot: dir, Dir: filepath.Join(dir, filepath.FromSlash(found.PackageDir))}, nil
}
func captureLinkInside(root, name string) error {
	// Compare filesystem identities: the source root may itself use a symlink
	// alias, such as macOS's /var -> /private/var temporary-directory path.
	canonicalRoot, err := pathutil.CanonicalExisting(root)
	if err != nil {
		return fmt.Errorf("captured CEL source root: %w", err)
	}
	resolved, err := pathutil.CanonicalExisting(filepath.Join(root, name))
	if err != nil {
		return fmt.Errorf("captured CEL symlink %q: %w", name, err)
	}
	rel, err := filepath.Rel(canonicalRoot, resolved)
	if err != nil || !safeCapturePath(filepath.ToSlash(rel), true) {
		return fmt.Errorf("captured CEL symlink %q escapes source", name)
	}
	return nil
}
func safeCaptureLink(name, target string) bool {
	if !safeCapturePath(name, false) || target == "" || filepath.IsAbs(target) || strings.Contains(target, "\\") {
		return false
	}
	return safeCapturePath(filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(name), target))), true)
}
func safeCapturePath(path string, allowRoot bool) bool {
	if allowRoot && path == "." {
		return true
	}
	return path != "" && path != "." && path != ".." && !strings.Contains(path, "\\") && !filepath.IsAbs(path) && filepath.ToSlash(filepath.Clean(path)) == path && !strings.HasPrefix(path, "../")
}
