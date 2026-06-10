package testjob

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompileInlineRecipeSuite(t *testing.T) {
	root := t.TempDir()
	recipePath := writeTestRecipe(t, root)
	suitePath := filepath.Join(root, "suite.yaml")
	if err := os.WriteFile(suitePath, []byte("cases:\n  - id: c1\n    type: recipe_case\n"), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	ir, err := Compile(context.Background(), Options{
		RecipeFile: recipePath,
		FilePath:   suitePath,
		WorkingDir: root,
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Compile(): %v", err)
	}
	if ir.TargetRecipe.Mode != "inline_recipe" {
		t.Fatalf("target mode = %q, want inline_recipe", ir.TargetRecipe.Mode)
	}
	if len(ir.Cases) != 1 || ir.Cases[0].ID != "c1" {
		t.Fatalf("cases = %#v", ir.Cases)
	}
}

func TestCompileRecipeFileExpandsLocalInlineInclude(t *testing.T) {
	root := t.TempDir()
	recipePath, suitePath := writeInlineIncludeRecipeFixture(t, root)

	ir, err := Compile(context.Background(), Options{
		RecipeFile: recipePath,
		FilePath:   suitePath,
		WorkingDir: root,
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Compile(): %v", err)
	}
	if !ir.TargetRecipe.Expanded {
		t.Fatalf("target recipe was not marked expanded")
	}
	if strings.Contains(ir.TargetRecipe.Content, "include: ./child.yaml") {
		t.Fatalf("compiled target still contains authored include:\n%s", ir.TargetRecipe.Content)
	}
	if !strings.Contains(ir.TargetRecipe.Content, "__c2j_internal") {
		t.Fatalf("compiled target missing inline provenance metadata:\n%s", ir.TargetRecipe.Content)
	}
}

func TestValidateAndRunRecipeFileWithLocalInlineInclude(t *testing.T) {
	root := t.TempDir()
	recipePath, suitePath := writeInlineIncludeRecipeFixture(t, root)
	outDir := filepath.Join(root, "out")

	validateStdout := &bytes.Buffer{}
	if err := Validate(context.Background(), Options{
		RecipeFile:  recipePath,
		FilePath:    suitePath,
		WorkingDir:  root,
		Parallelism: 1,
		Stdout:      validateStdout,
		Stderr:      &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Validate(): %v; stdout=%s", err, validateStdout.String())
	}
	if !strings.Contains(validateStdout.String(), "parent-inline valid") {
		t.Fatalf("stdout = %q, want validation completion", validateStdout.String())
	}

	runStdout := &bytes.Buffer{}
	if err := Run(context.Background(), Options{
		RecipeFile:  recipePath,
		FilePath:    suitePath,
		OutDir:      outDir,
		WorkingDir:  root,
		Parallelism: 1,
		Stdout:      runStdout,
		Stderr:      &bytes.Buffer{},
		Execution:   ExecutionOptions{ArtifactMode: "inline"},
	}); err != nil {
		t.Fatalf("Run(): %v; stdout=%s", err, runStdout.String())
	}
	if !strings.Contains(runStdout.String(), "parent-inline passed") {
		t.Fatalf("stdout = %q, want run completion", runStdout.String())
	}
}

func TestCompileScenarioMarkdownAndCaseFilter(t *testing.T) {
	root := t.TempDir()
	recipePath := writeTestRecipe(t, root)
	suitePath := filepath.Join(root, "suite.md")
	suite := "notes\n\n```yaml\ncases:\n  - id: c1\n    type: recipe_case\n  - id: c2\n    type: recipe_case\n```\n"
	if err := os.WriteFile(suitePath, []byte(suite), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	ir, err := Compile(context.Background(), Options{
		RecipeFile: recipePath,
		FilePath:   suitePath,
		CaseIDs:    []string{"c2"},
		WorkingDir: root,
		Stdout:     &bytes.Buffer{},
		Stderr:     &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Compile(): %v", err)
	}
	if len(ir.Cases) != 1 || ir.Cases[0].ID != "c2" {
		t.Fatalf("cases = %#v, want only c2", ir.Cases)
	}
}

func TestCompileAndWriteWritesCanonicalIR(t *testing.T) {
	root := t.TempDir()
	recipePath := writeTestRecipe(t, root)
	suitePath := filepath.Join(root, "suite.yaml")
	outPath := filepath.Join(root, "compiled.json")
	if err := os.WriteFile(suitePath, []byte("cases:\n  - id: c1\n    type: recipe_case\n"), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	stdout := &bytes.Buffer{}
	if err := CompileAndWrite(context.Background(), Options{
		RecipeFile: recipePath,
		FilePath:   suitePath,
		OutPath:    outPath,
		WorkingDir: root,
		Stdout:     stdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("CompileAndWrite(): %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("compiled output missing: %v", err)
	}
	if !strings.Contains(stdout.String(), "compiled 1 case") {
		t.Fatalf("stdout = %q, want compile summary", stdout.String())
	}
}

func TestValidateRunsLocally(t *testing.T) {
	root := t.TempDir()
	recipePath := writeTestRecipe(t, root)
	suitePath := filepath.Join(root, "suite.yaml")
	if err := os.WriteFile(suitePath, []byte("cases:\n  - id: c1\n    type: recipe_case\n"), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	stdout := &bytes.Buffer{}
	if err := Validate(context.Background(), Options{
		RecipeFile: recipePath,
		FilePath:   suitePath,
		WorkingDir: root,
		Stdout:     stdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("Validate(): %v; stdout=%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "c1 valid") {
		t.Fatalf("stdout = %q, want validation completion", stdout.String())
	}
}

func TestRunWritesArtifacts(t *testing.T) {
	root := t.TempDir()
	recipePath := writeTestRecipe(t, root)
	suitePath := filepath.Join(root, "suite.yaml")
	outDir := filepath.Join(root, "out")
	suite := `
cases:
  - id: c1
    type: recipe_case
    mocks:
      ops:
        - match:
            op: input
          behavior:
            mode: return
            outputs:
              response: ok
            artifacts:
              log.txt: hello
    assertions:
      - type: output_equals
        path: response
        value: ok
      - type: artifact_exists
        path: log.txt
`
	if err := os.WriteFile(suitePath, []byte(suite), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	stdout := &bytes.Buffer{}
	err := Run(context.Background(), Options{
		RecipeFile: recipePath,
		FilePath:   suitePath,
		OutDir:     outDir,
		WorkingDir: root,
		Stdout:     stdout,
		Stderr:     &bytes.Buffer{},
		Execution: ExecutionOptions{
			ArtifactMode: "inline",
		},
	})
	if err != nil {
		t.Fatalf("Run(): %v; stdout=%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "c1 passed") {
		t.Fatalf("stdout = %q, want case completion", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "summary.json")); err != nil {
		t.Fatalf("summary missing: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(outDir, "cases", "c1", "artifacts", "log.txt"))
	if err != nil {
		t.Fatalf("artifact missing: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("artifact content = %q, want hello", string(content))
	}
}

func TestRunMockedChoiceInput(t *testing.T) {
	root := t.TempDir()
	recipePath := writeChoiceTestRecipe(t, root)
	suitePath := filepath.Join(root, "suite.yaml")
	outDir := filepath.Join(root, "out")
	suite := `
cases:
  - id: choice
    type: recipe_case
    mocks:
      ops:
        - match:
            op: input
          behavior:
            mode: return
            outputs:
              response: cancel
    assertions:
      - type: output_equals
        path: response
        value: cancel
`
	if err := os.WriteFile(suitePath, []byte(suite), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	stdout := &bytes.Buffer{}
	err := Run(context.Background(), Options{
		RecipeFile:  recipePath,
		FilePath:    suitePath,
		OutDir:      outDir,
		WorkingDir:  root,
		Parallelism: 1,
		Stdout:      stdout,
		Stderr:      &bytes.Buffer{},
		Execution:   ExecutionOptions{ArtifactMode: "none"},
	})
	if err != nil {
		t.Fatalf("Run(): %v; stdout=%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "choice passed") {
		t.Fatalf("stdout = %q, want choice completion", stdout.String())
	}
}

func TestRunStopOnFailureStopsScheduling(t *testing.T) {
	root := t.TempDir()
	recipePath := writeTestRecipe(t, root)
	suitePath := filepath.Join(root, "suite.yaml")
	outDir := filepath.Join(root, "out")
	suite := `
cases:
  - id: c1
    type: recipe_case
    mocks:
      ops:
        - match:
            op: input
          behavior:
            mode: return
            outputs:
              response: bad
    assertions:
      - type: output_equals
        path: response
        value: ok
  - id: c2
    type: recipe_case
    mocks:
      ops:
        - match:
            op: input
          behavior:
            mode: return
            outputs:
              response: ok
`
	if err := os.WriteFile(suitePath, []byte(suite), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}

	stdout := &bytes.Buffer{}
	err := Run(context.Background(), Options{
		RecipeFile:    recipePath,
		FilePath:      suitePath,
		OutDir:        outDir,
		WorkingDir:    root,
		Parallelism:   1,
		StopOnFailure: true,
		Stdout:        stdout,
		Stderr:        &bytes.Buffer{},
		Execution:     ExecutionOptions{ArtifactMode: "none"},
	})
	if err == nil {
		t.Fatal("expected run to fail")
	}
	if !strings.Contains(stdout.String(), "c1 failed") {
		t.Fatalf("stdout = %q, want c1 failure", stdout.String())
	}
	if strings.Contains(stdout.String(), "c2 ") {
		t.Fatalf("stdout = %q, did not expect c2 to be scheduled", stdout.String())
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "cases", "c2", "result.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected c2 result not to exist, stat err=%v", statErr)
	}
}

func writeTestRecipe(t *testing.T, root string) string {
	t.Helper()
	recipePath := filepath.Join(root, "recipe.yaml")
	raw := "version: '1.0'\nid: x\nop: input\ninputs:\n  form:\n    question: q\n    type: short_answer\n"
	if err := os.WriteFile(recipePath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	return recipePath
}

func writeChoiceTestRecipe(t *testing.T, root string) string {
	t.Helper()
	recipePath := filepath.Join(root, "choice-recipe.yaml")
	raw := `
version: '1.0'
id: choice-input
op: input
inputs:
  form:
    question: Continue?
    type: multiple_choice
    options:
      - value: continue
        label: Continue
      - value: cancel
        label: Cancel
`
	if err := os.WriteFile(recipePath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	return recipePath
}

func writeInlineIncludeRecipeFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	parentPath := filepath.Join(root, "parent.yaml")
	parent := `
id: parent
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
inputs:
  prompt: "{{ inputs.prompt }}"
sequence:
  - id: child
    include: ./child.yaml
    inputs:
      prompt: "{{ inputs.prompt }}"
outputs:
  message: "{{ sequence.child.outputs.message }}"
`
	if err := os.WriteFile(parentPath, []byte(parent), 0o644); err != nil {
		t.Fatalf("write parent recipe: %v", err)
	}

	childPath := filepath.Join(root, "child.yaml")
	child := `
id: child
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
inputs:
  prompt: "{{ inputs.prompt }}"
sequence:
  - id: ask
    op: input
    inputs:
      form:
        question: "{{ inputs.prompt }}"
        type: short_answer
outputs:
  message: "{{ sequence.ask.outputs.response }}"
`
	if err := os.WriteFile(childPath, []byte(child), 0o644); err != nil {
		t.Fatalf("write child recipe: %v", err)
	}

	suitePath := filepath.Join(root, "parent.scenario.md")
	suite := `
` + "```yaml" + `
cases:
  - id: parent-inline
    type: recipe_case
    inputs:
      prompt: hello
    mocks:
      ops:
        - match:
            op: input
          behavior:
            mode: return
            outputs:
              response: hello
    assertions:
      - type: output_equals
        path: message
        value: hello
` + "```" + `
`
	if err := os.WriteFile(suitePath, []byte(suite), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	return parentPath, suitePath
}
