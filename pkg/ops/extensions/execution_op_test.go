package extensions

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreops "github.com/colony-2/c2j/pkg/ops"
)

func TestExecutionOpRunsLocalSelector(t *testing.T) {
	tmpDir := t.TempDir()
	opDir := filepath.Join(tmpDir, "testdata", "echo-op")
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatalf("mkdir op dir: %v", err)
	}
	opYAML := `
name: echo
shell: bash
run: cat
input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
output_schema:
  type: object
  properties:
    message:
      type: string
`
	if err := os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(opYAML), 0o644); err != nil {
		t.Fatalf("write op.yaml: %v", err)
	}

	deps := coreops.NewOpDependenciesBuilder().
		WithWorktreePath(tmpDir).
		Build()

	output, err := GetExecutionOp().TaskChain()[0].Invoke(deps, context.Background(), map[string]interface{}{
		"selector": "./testdata/echo-op",
		"inputs": map[string]interface{}{
			"message": "hello",
		},
	})
	if err != nil {
		t.Fatalf("invoke selector op: %v", err)
	}
	if got := output["message"]; got != "hello" {
		t.Fatalf("expected echoed message, got %#v", got)
	}
}

func TestResolveRejectsRemovedOrMissingManifestFields(t *testing.T) {
	tests := []struct {
		name        string
		manifest    string
		errContains string
	}{
		{
			name: "removed args",
			manifest: `
name: bad
shell: sh
command: ["echo"]
args: ["hello"]
input_schema:
  type: object
  properties: {}
output_schema:
  type: object
  properties: {}
`,
			errContains: `manifest field "args" is not supported`,
		},
		{
			name: "removed working_directory",
			manifest: `
name: bad
shell: sh
run: echo hello
working_directory: .
input_schema:
  type: object
  properties: {}
output_schema:
  type: object
  properties: {}
`,
			errContains: `manifest field "working_directory" is not supported`,
		},
		{
			name: "missing input schema",
			manifest: `
name: bad
shell: sh
run: echo hello
output_schema:
  type: object
  properties: {}
`,
			errContains: `missing required input_schema`,
		},
		{
			name: "missing output schema",
			manifest: `
name: bad
shell: sh
run: echo hello
input_schema:
  type: object
  properties: {}
`,
			errContains: `missing required output_schema`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			opDir := filepath.Join(tmpDir, "testdata", "bad-op")
			if err := os.MkdirAll(opDir, 0o755); err != nil {
				t.Fatalf("mkdir op dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(tt.manifest), 0o644); err != nil {
				t.Fatalf("write op.yaml: %v", err)
			}

			_, err := Resolve(context.Background(), "./testdata/bad-op", ResolveOptions{BaseDir: tmpDir})
			if err == nil {
				t.Fatalf("expected resolve error containing %q", tt.errContains)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Fatalf("expected resolve error containing %q, got %v", tt.errContains, err)
			}
		})
	}
}

func TestResolveGitSelectorUsesSharedSourceCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	repoDir := t.TempDir()
	runExtensionGit(t, "", "init", "--initial-branch", "main", repoDir)
	runExtensionGit(t, repoDir, "config", "user.email", "extension-cache-test@example.com")
	runExtensionGit(t, repoDir, "config", "user.name", "Extension Cache Test")

	opDir := filepath.Join(repoDir, "tools", "ops", "echo")
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatalf("mkdir op dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(`
name: echo
shell: sh
run: cat
input_schema:
  type: object
  properties: {}
output_schema:
  type: object
  properties: {}
`), 0o644); err != nil {
		t.Fatalf("write op manifest: %v", err)
	}
	runExtensionGit(t, repoDir, "add", ".")
	runExtensionGit(t, repoDir, "commit", "-m", "add extension op")
	commit := strings.TrimSpace(runExtensionGitOutput(t, repoDir, "rev-parse", "HEAD"))

	repoURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(repoDir)}).String()
	selector := "git+" + repoURL + "//tools/ops/echo@HEAD"
	resolved, err := ResolvePath(context.Background(), selector, ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve git selector: %v", err)
	}
	if resolved.ResolvedCommit != commit {
		t.Fatalf("resolved commit = %q, want %q", resolved.ResolvedCommit, commit)
	}
	wantResolved := "git+" + repoURL + "//tools/ops/echo@" + commit
	if resolved.ResolvedSelector != wantResolved {
		t.Fatalf("resolved selector = %q, want %q", resolved.ResolvedSelector, wantResolved)
	}
	if wantDir := filepath.Join(resolved.ProjectRoot, "tools", "ops", "echo"); resolved.Dir != wantDir {
		t.Fatalf("resolved dir = %q, want %q", resolved.Dir, wantDir)
	}
	if !strings.Contains(resolved.ProjectRoot, filepath.Join(".c2j", "cache", "git-selectors", "v1", "sources")) {
		t.Fatalf("project root %q does not use shared selector cache", resolved.ProjectRoot)
	}

	if err := os.Rename(repoDir, repoDir+".gone"); err != nil {
		t.Fatalf("rename remote repo away: %v", err)
	}
	pinned := "git+" + repoURL + "//tools/ops/echo@" + commit
	resolvedPinned, err := ResolvePath(context.Background(), pinned, ResolveOptions{})
	if err != nil {
		t.Fatalf("resolve pinned git selector from cache: %v", err)
	}
	if resolvedPinned.ProjectRoot != resolved.ProjectRoot {
		t.Fatalf("pinned project root = %q, want %q", resolvedPinned.ProjectRoot, resolved.ProjectRoot)
	}
	if resolvedPinned.ResolvedSelector != pinned {
		t.Fatalf("pinned resolved selector = %q, want %q", resolvedPinned.ResolvedSelector, pinned)
	}
}

func TestExecutionOpHonorsManifestTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	opDir := filepath.Join(tmpDir, "testdata", "slow-op")
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatalf("mkdir op dir: %v", err)
	}
	opYAML := `
name: slow
shell: sh
run: exec sleep 1
timeout: 10ms
input_schema:
  type: object
  properties: {}
output_schema:
  type: object
  properties: {}
`
	if err := os.WriteFile(filepath.Join(opDir, "op.yaml"), []byte(opYAML), 0o644); err != nil {
		t.Fatalf("write op.yaml: %v", err)
	}

	deps := coreops.NewOpDependenciesBuilder().
		WithWorktreePath(tmpDir).
		Build()

	start := time.Now()
	_, err := GetExecutionOp().TaskChain()[0].Invoke(deps, context.Background(), map[string]interface{}{
		"selector": "./testdata/slow-op",
		"inputs":   map[string]interface{}{},
	})
	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("expected timeout to fire well before command completion, elapsed=%s err=%v", elapsed, err)
	}
}

func runExtensionGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	_ = runExtensionGitOutput(t, dir, args...)
}

func runExtensionGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
