package extensions

import (
	"context"
	"os"
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
