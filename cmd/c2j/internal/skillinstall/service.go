package skillinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	yamlv3 "gopkg.in/yaml.v3"
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type LockFile struct {
	Version int         `yaml:"version"`
	Skills  []LockEntry `yaml:"skills,omitempty"`
}

type LockEntry struct {
	Name      string `yaml:"name"`
	Source    string `yaml:"source"`
	Ref       string `yaml:"ref"`
	Commit    string `yaml:"commit,omitempty"`
	SkillPath string `yaml:"skill_path"`
	Scope     string `yaml:"scope"`
	Path      string `yaml:"path"`
	Digest    string `yaml:"digest"`
}

func InstallBundled(ctx context.Context, opts Options) (Summary, error) {
	_ = ctx

	if opts.Bundle == nil {
		return Summary{}, fmt.Errorf("skill bundle is required")
	}

	workingDir, err := resolveWorkingDir(opts.WorkingDir)
	if err != nil {
		return Summary{}, err
	}
	scope, err := normalizeScope(opts.Scope)
	if err != nil {
		return Summary{}, err
	}
	dstRoot, err := destinationRoot(workingDir, scope)
	if err != nil {
		return Summary{}, err
	}

	lockPath := filepath.Join(workingDir, ".c2j", "skills-lock.yaml")
	lock, err := readLockFile(lockPath)
	if err != nil {
		return Summary{}, err
	}

	skillNames, err := bundledSkillNames(opts.Bundle)
	if err != nil {
		return Summary{}, err
	}

	summary := Summary{}
	for _, name := range skillNames {
		result := installOneBundled(opts.Bundle, lock, dstRoot, scope, name, opts.Force)
		summary.Results = append(summary.Results, result)
		if result.Action == ActionInstalled || (result.Action == ActionSkipped && result.Reason == "same content digest") {
			lock.upsert(LockEntry{
				Name:      name,
				Source:    "c2j",
				Ref:       "bundled",
				SkillPath: path.Join("skills", name),
				Scope:     scope,
				Path:      filepath.ToSlash(result.Path),
				Digest:    result.Digest,
			})
		}
	}

	if opts.IncludeOpSkills {
		reason := "external c2ops op-specific skill install is not enabled in this build"
		if _, err := LoadSources(opts.Bundle); err != nil {
			reason = "external c2ops op-specific skill install skipped: " + err.Error()
		}
		summary.Results = append(summary.Results, SkillResult{
			Name:   "c2ops",
			Action: ActionSkipped,
			Reason: reason,
		})
	}

	if err := writeLockFile(lockPath, lock); err != nil {
		return summary, err
	}
	if summary.HasFailures() {
		return summary, fmt.Errorf("install bundled skills: %d failed", len(summary.Failed()))
	}
	return summary, nil
}

func installOneBundled(bundle fs.FS, lock LockFile, dstRoot string, scope string, name string, force bool) SkillResult {
	dst := filepath.Join(dstRoot, name)

	if err := ValidateSkillDir(bundle, name); err != nil {
		return SkillResult{Name: name, Action: ActionFailed, Path: dst, Err: err}
	}
	digest, err := DigestSkillDir(bundle, name)
	if err != nil {
		return SkillResult{Name: name, Action: ActionFailed, Path: dst, Err: err}
	}

	info, err := os.Stat(dst)
	if err != nil && !os.IsNotExist(err) {
		return SkillResult{Name: name, Action: ActionFailed, Path: dst, Digest: digest, Err: fmt.Errorf("stat installed skill: %w", err)}
	}
	if err == nil {
		existingDigest, digestErr := DigestOSDir(dst)
		if digestErr != nil {
			if !force {
				return SkillResult{Name: name, Action: ActionSkipped, Path: dst, Digest: digest, Reason: digestErr.Error()}
			}
		} else if existingDigest == digest {
			return SkillResult{Name: name, Action: ActionSkipped, Path: dst, Digest: digest, Reason: "same content digest"}
		} else if !force {
			locked, ok := lock.find(name, scope)
			if !ok || locked.Digest != existingDigest {
				return SkillResult{Name: name, Action: ActionSkipped, Path: dst, Digest: digest, Reason: "existing skill has local modifications"}
			}
		}
		if !info.IsDir() && !force {
			return SkillResult{Name: name, Action: ActionSkipped, Path: dst, Digest: digest, Reason: "existing path is not a directory"}
		}
	}

	if err := copySkillDir(bundle, name, dst); err != nil {
		return SkillResult{Name: name, Action: ActionFailed, Path: dst, Digest: digest, Err: err}
	}
	return SkillResult{Name: name, Action: ActionInstalled, Path: dst, Digest: digest}
}

func bundledSkillNames(bundle fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(bundle, ".")
	if err != nil {
		return nil, fmt.Errorf("read skill bundle: %w", err)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, err := fs.Stat(bundle, path.Join(name, "SKILL.md")); err == nil {
			names = append(names, name)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("stat %s/SKILL.md: %w", name, err)
		}
	}
	sort.Strings(names)
	return names, nil
}

func ValidateSkillDir(fsys fs.FS, name string) error {
	if err := validateSkillName(name); err != nil {
		return err
	}
	if err := validateSkillTree(fsys, name, MaxSkillFileBytes); err != nil {
		return err
	}
	raw, err := fs.ReadFile(fsys, path.Join(name, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("read SKILL.md: %w", err)
	}
	meta, err := parseSkillFrontmatter(raw)
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(meta["name"]); got != name {
		return fmt.Errorf("SKILL.md name %q does not match folder %q", got, name)
	}
	if strings.TrimSpace(meta["description"]) == "" {
		return fmt.Errorf("SKILL.md description is required")
	}
	return nil
}

func validateSkillName(name string) error {
	if len(name) == 0 || len(name) > 64 || !skillNamePattern.MatchString(name) {
		return fmt.Errorf("invalid skill folder name %q", name)
	}
	return nil
}

func parseSkillFrontmatter(raw []byte) (map[string]string, error) {
	text := string(raw)
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("SKILL.md must start with YAML frontmatter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("SKILL.md frontmatter is not closed")
	}
	frontmatter := rest[:end]
	var data map[string]string
	if err := yamlv3.Unmarshal([]byte(frontmatter), &data); err != nil {
		return nil, fmt.Errorf("parse SKILL.md frontmatter: %w", err)
	}
	if len(data) != 2 {
		return nil, fmt.Errorf("SKILL.md frontmatter must contain only name and description")
	}
	if _, ok := data["name"]; !ok {
		return nil, fmt.Errorf("SKILL.md frontmatter missing name")
	}
	if _, ok := data["description"]; !ok {
		return nil, fmt.Errorf("SKILL.md frontmatter missing description")
	}
	return data, nil
}

func validateSkillTree(fsys fs.FS, root string, maxFileBytes int64) error {
	return fs.WalkDir(fsys, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := secureRelativePath(root, name)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlinks are not allowed", rel)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("%s: stat: %w", rel, err)
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: only regular files are allowed", rel)
		}
		if info.Size() > maxFileBytes {
			return fmt.Errorf("%s: file exceeds %d bytes", rel, maxFileBytes)
		}
		return nil
	})
}

func DigestSkillDir(fsys fs.FS, root string) (string, error) {
	if err := validateSkillTree(fsys, root, MaxSkillFileBytes); err != nil {
		return "", err
	}
	h := sha256.New()
	err := fs.WalkDir(fsys, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := secureRelativePath(root, name)
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		_, _ = h.Write([]byte(filepath.ToSlash(rel)))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func DigestOSDir(dir string) (string, error) {
	return DigestSkillDir(os.DirFS(dir), ".")
}

func secureRelativePath(root string, name string) (string, error) {
	root = path.Clean(root)
	name = path.Clean(name)
	rel := name
	if root == "." {
		rel = strings.TrimPrefix(name, "./")
	} else if name == root {
		rel = "."
	} else if strings.HasPrefix(name, root+"/") {
		rel = strings.TrimPrefix(name, root+"/")
	} else {
		return "", fmt.Errorf("%s escapes skill directory", name)
	}
	if rel == "." {
		return rel, nil
	}
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return "", fmt.Errorf("%s escapes skill directory", name)
	}
	return clean, nil
}

func copySkillDir(fsys fs.FS, root string, dst string) error {
	dstRoot := filepath.Dir(dst)
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dstRoot, err)
	}
	tmp, err := os.MkdirTemp(dstRoot, "."+filepath.Base(dst)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary skill dir: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmp)
		}
	}()

	if err := fs.WalkDir(fsys, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := secureRelativePath(root, name)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: only regular files are allowed", rel)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		src, err := fsys.Open(name)
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, src)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		return err
	}

	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("remove existing skill %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("install skill %s: %w", dst, err)
	}
	cleanup = false
	return nil
}

func resolveWorkingDir(workingDir string) (string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = cwd
	}
	abs, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", workingDir, err)
	}
	return abs, nil
}

func normalizeScope(scope string) (string, error) {
	switch strings.TrimSpace(scope) {
	case "", ScopeAuto:
		return ScopeProject, nil
	case ScopeProject:
		return ScopeProject, nil
	case ScopeUser:
		return ScopeUser, nil
	default:
		return "", fmt.Errorf("unsupported skills scope %q", scope)
	}
}

func destinationRoot(workingDir string, scope string) (string, error) {
	switch scope {
	case ScopeProject:
		return filepath.Join(workingDir, ".agents", "skills"), nil
	case ScopeUser:
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve user home: %w", err)
			}
			codexHome = filepath.Join(home, ".codex")
		}
		return filepath.Join(codexHome, "skills"), nil
	default:
		return "", fmt.Errorf("unsupported skills scope %q", scope)
	}
}

func readLockFile(lockPath string) (LockFile, error) {
	raw, err := os.ReadFile(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return LockFile{Version: 1}, nil
	}
	if err != nil {
		return LockFile{}, fmt.Errorf("read %s: %w", lockPath, err)
	}
	lock := LockFile{Version: 1}
	if err := yamlv3.Unmarshal(raw, &lock); err != nil {
		return LockFile{}, fmt.Errorf("parse %s: %w", lockPath, err)
	}
	if lock.Version == 0 {
		lock.Version = 1
	}
	return lock, nil
}

func writeLockFile(lockPath string, lock LockFile) error {
	lock.Version = 1
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(lockPath), err)
	}
	sort.SliceStable(lock.Skills, func(i, j int) bool {
		if lock.Skills[i].Name == lock.Skills[j].Name {
			return lock.Skills[i].Scope < lock.Skills[j].Scope
		}
		return lock.Skills[i].Name < lock.Skills[j].Name
	})
	raw, err := yamlv3.Marshal(lock)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", lockPath, err)
	}
	return os.WriteFile(lockPath, raw, 0o644)
}

func (l LockFile) find(name string, scope string) (LockEntry, bool) {
	for _, entry := range l.Skills {
		if entry.Name == name && entry.Scope == scope {
			return entry, true
		}
	}
	return LockEntry{}, false
}

func (l *LockFile) upsert(entry LockEntry) {
	for i := range l.Skills {
		if l.Skills[i].Name == entry.Name && l.Skills[i].Scope == entry.Scope {
			l.Skills[i] = entry
			return
		}
	}
	l.Skills = append(l.Skills, entry)
}
