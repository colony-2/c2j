package extensions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
