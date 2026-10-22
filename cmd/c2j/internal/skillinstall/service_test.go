package skillinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	skillsbundle "github.com/colony-2/c2j/skills"
)

func TestInstallBundledProjectCopiesSkillsAndWritesLock(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	summary, err := InstallBundled(t.Context(), Options{
		Bundle:     skillsbundle.FS,
		WorkingDir: root,
		Scope:      ScopeProject,
	})
	if err != nil {
		t.Fatalf("InstallBundled(): %v", err)
	}
	if got := len(summary.Installed()); got != 3 {
		t.Fatalf("installed count = %d, want 3; summary=%#v", got, summary)
	}

	for _, name := range []string{"c2j-operations", "c2j-recipes", "c2ops-extension-ops"} {
		if _, err := os.Stat(filepath.Join(root, ".agents", "skills", name, "SKILL.md")); err != nil {
			t.Fatalf("expected %s to be installed: %v", name, err)
		}
	}

	lock, err := readLockFile(filepath.Join(root, ".c2", "skills-lock.yaml"))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if len(lock.Skills) != 3 {
		t.Fatalf("lock skills = %d, want 3: %#v", len(lock.Skills), lock.Skills)
	}
	for _, entry := range lock.Skills {
		if want := ".agents/skills/" + entry.Name; entry.Path != want {
			t.Fatalf("lock path = %q, want %q", entry.Path, want)
		}
		if entry.Source != "c2j" || entry.Ref != "bundled" || entry.SkillPath == "" || entry.Path == "" || !strings.HasPrefix(entry.Digest, "sha256:") {
			t.Fatalf("incomplete lock entry: %#v", entry)
		}
	}
}

func TestInstallBundledSameDigestIsSkipped(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: root}); err != nil {
		t.Fatalf("initial InstallBundled(): %v", err)
	}
	summary, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: root})
	if err != nil {
		t.Fatalf("second InstallBundled(): %v", err)
	}
	if got := len(summary.Skipped()); got != 3 {
		t.Fatalf("skipped count = %d, want 3; summary=%#v", got, summary)
	}
	for _, result := range summary.Skipped() {
		if result.Reason != "same content digest" {
			t.Fatalf("skip reason for %s = %q", result.Name, result.Reason)
		}
	}
}

func TestInstallBundledModifiedSkillSkippedWithoutForce(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: root}); err != nil {
		t.Fatalf("initial InstallBundled(): %v", err)
	}
	skillPath := filepath.Join(root, ".agents", "skills", "c2j-recipes", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: c2j-recipes\ndescription: local\n---\n\nlocal change\n"), 0o644); err != nil {
		t.Fatalf("modify skill: %v", err)
	}

	summary, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: root})
	if err != nil {
		t.Fatalf("InstallBundled(): %v", err)
	}
	found := false
	for _, result := range summary.Skipped() {
		if result.Name == "c2j-recipes" {
			found = true
			if result.Reason != "existing skill has local modifications" {
				t.Fatalf("reason = %q", result.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("c2j-recipes was not skipped: %#v", summary)
	}
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if !strings.Contains(string(raw), "local change") {
		t.Fatalf("local change was overwritten without force")
	}
}

func TestInstallBundledForceOverwritesModifiedSkill(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: root}); err != nil {
		t.Fatalf("initial InstallBundled(): %v", err)
	}
	skillPath := filepath.Join(root, ".agents", "skills", "c2j-recipes", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\nname: c2j-recipes\ndescription: local\n---\n\nlocal change\n"), 0o644); err != nil {
		t.Fatalf("modify skill: %v", err)
	}

	summary, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: root, Force: true})
	if err != nil {
		t.Fatalf("InstallBundled(force): %v", err)
	}
	found := false
	for _, result := range summary.Installed() {
		if result.Name == "c2j-recipes" {
			found = true
		}
	}
	if !found {
		t.Fatalf("c2j-recipes was not reinstalled: %#v", summary)
	}
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatalf("read skill: %v", err)
	}
	if strings.Contains(string(raw), "local change") {
		t.Fatalf("local change survived force overwrite")
	}
}

func TestInstallBundledUserScopeUsesCodexHome(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	if _, err := InstallBundled(t.Context(), Options{
		Bundle:     skillsbundle.FS,
		WorkingDir: filepath.Join(root, "project"),
		Scope:      ScopeUser,
	}); err != nil {
		t.Fatalf("InstallBundled(user): %v", err)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "skills", "c2j-recipes", "SKILL.md")); err != nil {
		t.Fatalf("expected user skill install: %v", err)
	}
}

func TestBundledSkillValidation(t *testing.T) {
	t.Parallel()

	names, err := bundledSkillNames(skillsbundle.FS)
	if err != nil {
		t.Fatalf("bundledSkillNames(): %v", err)
	}
	if len(names) != 3 {
		t.Fatalf("skill names = %v, want 3 bundled skills", names)
	}
	for _, name := range names {
		if err := ValidateSkillDir(skillsbundle.FS, name); err != nil {
			t.Fatalf("ValidateSkillDir(%s): %v", name, err)
		}
	}
}

func TestLoadSourcesTrustsC2OpsAndRejectsUnlistedRepos(t *testing.T) {
	t.Parallel()

	sources, err := LoadSources(skillsbundle.FS)
	if err != nil {
		t.Fatalf("LoadSources(): %v", err)
	}
	if name, ok, err := sources.TrustedGitHubSkill("colony-2", "c2ops", "main", "skills/c2ops-codex"); err != nil || !ok || name != "c2ops" {
		t.Fatalf("TrustedGitHubSkill(c2ops) = name=%q ok=%v err=%v", name, ok, err)
	}
	if _, ok, err := sources.TrustedGitHubSkill("someone", "else", "main", "skills/c2ops-codex"); err != nil || ok {
		t.Fatalf("unlisted repo trust = ok=%v err=%v, want false nil", ok, err)
	}
	if name, ok, err := sources.TrustedSelector("git+https://github.com/colony-2/c2ops.git//codex@main"); err != nil || !ok || name != "c2ops" {
		t.Fatalf("TrustedSelector(c2ops) = name=%q ok=%v err=%v", name, ok, err)
	}
	if _, ok, err := sources.TrustedSelector("git+https://github.com/someone/else.git//codex@main"); err != nil || ok {
		t.Fatalf("unlisted selector trust = ok=%v err=%v, want false nil", ok, err)
	}
}

func TestValidateSkillDirRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	t.Parallel()

	root := t.TempDir()
	skillDir := filepath.Join(root, "bad-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: bad-skill\ndescription: bad\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.Symlink("/tmp", filepath.Join(skillDir, "escape")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := ValidateSkillDir(os.DirFS(root), "bad-skill"); err == nil || !strings.Contains(err.Error(), "symlinks are not allowed") {
		t.Fatalf("ValidateSkillDir() err = %v, want symlink rejection", err)
	}
}

func TestPublishedC2OpsSelectors(t *testing.T) {
	sources, err := LoadSources(skillsbundle.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{
		"nix:github:colony-2/c2ops/published#codex",
		"nix:github:colony-2/c2ops/published#skill-run",
		"nix:github:colony-2/c2ops/v1.0.0#jev",
		"git+https://github.com/colony-2/c2ops.git//kimi@published",
	} {
		if name, ok, err := sources.TrustedSelector(selector); err != nil || !ok || name != "c2ops" {
			t.Errorf("%s: name=%q trusted=%v err=%v", selector, name, ok, err)
		}
	}
	for _, selector := range []string{
		"nix:github:someone/c2ops/published#codex",
		"nix:github:colony-2/c2ops-other/published#codex",
		"nix:github:colony-2/c2ops/untrusted#codex",
		"nix:github:colony-2/c2ops/published",
		"nix:github:colony-2/c2ops/published#",
		"nix:github:colony-2/c2ops/#codex",
	} {
		if _, ok, _ := sources.TrustedSelector(selector); ok {
			t.Errorf("unexpectedly trusted %s", selector)
		}
	}
}

func TestProjectLockSurvivesRelocation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldRoot, newRoot := filepath.Join(root, "original"), filepath.Join(root, "moved")
	if _, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: oldRoot}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(oldRoot, ".c2", "skills-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRoot, newRoot); err != nil {
		t.Fatal(err)
	}
	summary, err := InstallBundled(t.Context(), Options{Bundle: skillsbundle.FS, WorkingDir: newRoot})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Skipped()) != 3 {
		t.Fatalf("expected unchanged skills after relocation: %#v", summary)
	}
	after, err := os.ReadFile(filepath.Join(newRoot, ".c2", "skills-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("relocating project changed the skills lock")
	}
}

func TestWriteLockUsesScopeRelativePaths(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "skills-lock.yaml")
	lock := LockFile{Skills: []LockEntry{
		{Name: "project-skill", Scope: ScopeProject, Path: "/old/project/.agents/skills/project-skill"},
		{Name: "user-skill", Scope: ScopeUser, Path: "/old/home/.codex/skills/user-skill"},
	}}
	if err := writeLockFile(lockPath, lock); err != nil {
		t.Fatal(err)
	}
	actual, err := readLockFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range actual.Skills {
		want := "skills/" + entry.Name
		if entry.Scope == ScopeProject {
			want = ".agents/" + want
		}
		if entry.Path != want {
			t.Fatalf("%s: path = %q, want %q", entry.Name, entry.Path, want)
		}
	}
}
