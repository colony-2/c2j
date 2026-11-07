package testjob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
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

func TestRecipeFileLocalInlineIncludeAcceptsBuiltinOp(t *testing.T) {
	root := t.TempDir()
	recipePath, suitePath := writeInlineIncludeBuiltinOpFixture(t, root)
	outDir := filepath.Join(root, "out")

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
	if !strings.Contains(ir.TargetRecipe.Content, "op: squashrebasemerge") {
		t.Fatalf("compiled target missing included builtin op:\n%s", ir.TargetRecipe.Content)
	}

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
	if !strings.Contains(validateStdout.String(), "parent-inline-child-merge-op valid") {
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
	if !strings.Contains(runStdout.String(), "parent-inline-child-merge-op passed") {
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

func TestValidatePrintsValidationDiagnostics(t *testing.T) {
	root := t.TempDir()
	recipePath, suitePath := writeSemanticValidationErrorFixture(t, root)

	stdout := &bytes.Buffer{}
	err := Validate(context.Background(), Options{
		RecipeFile:  recipePath,
		FilePath:    suitePath,
		WorkingDir:  root,
		Parallelism: 1,
		Stdout:      stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	out := stdout.String()
	if !strings.Contains(out, "semantic-error invalid") {
		t.Fatalf("stdout = %q, want invalid status", out)
	}
	if !strings.Contains(out, "semantic_validation target_recipe:") {
		t.Fatalf("stdout = %q, want semantic validation diagnostic", out)
	}
	if !strings.Contains(out, "failed to resolve input 'evidence'") {
		t.Fatalf("stdout = %q, want validation error detail", out)
	}
}

func TestValidateJSONIncludesValidationDiagnostics(t *testing.T) {
	root := t.TempDir()
	recipePath, suitePath := writeSemanticValidationErrorFixture(t, root)

	stdout := &bytes.Buffer{}
	err := Validate(context.Background(), Options{
		RecipeFile:  recipePath,
		FilePath:    suitePath,
		WorkingDir:  root,
		Parallelism: 1,
		JSONOutput:  true,
		Stdout:      stdout,
		Stderr:      &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("expected validation to fail")
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var summary validationSummary
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		t.Fatalf("decode validation summary: %v; stdout=%s", err, stdout.String())
	}
	if summary.Cases != 1 || summary.InvalidOrError != 1 {
		t.Fatalf("summary = %#v, want one invalid case", summary)
	}
	if len(summary.Results) != 1 || summary.Results[0].Validation == nil || len(summary.Results[0].Validation.Errors) != 1 {
		t.Fatalf("summary results missing validation errors: %#v", summary.Results)
	}
	issue := summary.Results[0].Validation.Errors[0]
	if issue.Code != "semantic_validation" || issue.Field != "target_recipe" {
		t.Fatalf("issue = %#v, want semantic target_recipe issue", issue)
	}
	if !strings.Contains(issue.Message, "failed to resolve input 'evidence'") {
		t.Fatalf("issue message = %q, want detail", issue.Message)
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

func writeSemanticValidationErrorFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	recipePath := filepath.Join(root, "semantic-error.yaml")
	recipe := `
version: "1.0"
id: semantic-error
state:
  initial: happy
  states:
    bad:
      sequence:
        - id: produce
          op: command_execution
          inputs:
            run: echo bad
      transitions: []
      outputs:
        evidence: "${{ sequence.produce.outputs.parsed_output.evidence }}"
    happy:
      op: command_execution
      transitions: []
      inputs:
        run: echo happy
outputs:
  status: ok
`
	if err := os.WriteFile(recipePath, []byte(recipe), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	suitePath := filepath.Join(root, "semantic-error.test.yaml")
	suite := `
cases:
  - id: semantic-error
    type: recipe_case
`
	if err := os.WriteFile(suitePath, []byte(suite), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	return recipePath, suitePath
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
    perms: {limit: {jobdb: {}}}
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

func writeInlineIncludeBuiltinOpFixture(t *testing.T, root string) (string, string) {
	t.Helper()
	childPath := filepath.Join(root, "child.yaml")
	child := `
id: child-with-merge-op
version: "1.0"
desc: Child recipe that uses a built-in merge op
state:
  initial: merge
  states:
    merge:
      op: squashrebasemerge
      transitions: []
      inputs:
        repo_path: "{{ context.environment.op.worktree_path }}"
        local_hash: "0000000000000000000000000000000000000000"
        upstream_repo: "git@example.com:org/repo.git"
        upstream_branch: "main"
        rebase: true
        author: ""
        commit_message: "test merge"
outputs:
  merged_hash: "${{ state_output('merge', 'merged_hash', '') }}"
`
	if err := os.WriteFile(childPath, []byte(child), 0o644); err != nil {
		t.Fatalf("write child recipe: %v", err)
	}

	parentPath := filepath.Join(root, "parent.yaml")
	parent := `
id: parent-inline-child-with-merge-op
version: "1.0"
desc: Parent recipe that inlines a child recipe using a built-in merge op
state:
  initial: child
  states:
    child:
      include: ./child.yaml
      perms: {limit: {jobdb: {}}}
      transitions: []
      inputs: {}
outputs:
  child_merged_hash: "${{ state_output('child', 'merged_hash', '') }}"
`
	if err := os.WriteFile(parentPath, []byte(parent), 0o644); err != nil {
		t.Fatalf("write parent recipe: %v", err)
	}

	suitePath := filepath.Join(root, "parent.scenario.md")
	suite := `
` + "```yaml" + `
cases:
  - id: parent-inline-child-merge-op
    type: recipe_case
    inputs: {}
    mocks:
      ops:
        - match:
            op: squashrebasemerge
          behavior:
            mode: return
            outputs:
              target_branch: main
              remote_ref: refs/heads/main
              merged_hash: abc123
              squashed_commits:
                base_hash: base123
                persist_hash: persist123
              git_context_patch: {}
              fast_forward: true
    assertions:
      - type: output_equals
        path: child_merged_hash
        value: abc123
` + "```" + `
`
	if err := os.WriteFile(suitePath, []byte(suite), 0o644); err != nil {
		t.Fatalf("write suite: %v", err)
	}
	return parentPath, suitePath
}

func TestNativeObservationBridgeReportAndArtifactRetrieval(t *testing.T) {
	root, err := filepath.Abs("../../../../testdata/observations")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "reports")
	opts := Options{FilePath: filepath.Join(root, "bridge.test.yaml"), WorkingDir: root, OutDir: out, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if err := Validate(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), opts); err != nil {
		b, _ := os.ReadFile(filepath.Join(out, "cases", "bounded-observation", "result.json"))
		t.Fatalf("%v: %s", err, b)
	}
	report, err := os.ReadFile(filepath.Join(out, "cases", "bounded-observation", "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(report), "fixture-secret") {
		t.Fatal("secret in report")
	}
	var decoded map[string]any
	if err := json.Unmarshal(report, &decoded); err != nil {
		t.Fatal(err)
	}
	run := decoded["run"].(map[string]any)
	runtime := run["runtime"].(map[string]any)
	schemaBytes, err := os.ReadFile(filepath.Join(root, "runtime-observation.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schemaDoc any
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		t.Fatal(err)
	}
	validator := jsonschema.NewCompiler()
	if err := validator.AddResource("urn:c2j:native-test:runtime-observations:v1", schemaDoc); err != nil {
		t.Fatal(err)
	}
	schema, err := validator.Compile("urn:c2j:native-test:runtime-observations:v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(runtime); err != nil {
		t.Fatal(err)
	}

	if runtime["observations_version"] != float64(1) || len(runtime["observations"].([]any)) != 4 {
		t.Fatalf("unexpected observations: %s", report)
	}
	diagnostic, err := os.ReadFile(filepath.Join(out, "cases", "bounded-observation", "artifacts", "observations", "observation-1", fmt.Sprintf("artifact-%x", sha256.Sum256([]byte("diagnostic.txt")))))
	if err != nil {
		t.Fatal(err)
	}
	if string(diagnostic) != "diagnostic [REDACTED]" {
		t.Fatalf("unexpected diagnostic %q", diagnostic)
	}
}

func TestNativeObservationEmptyDiagnosticRetrieval(t *testing.T) {
	root := t.TempDir()
	recipe := `id: empty
sequence:
- id: verify
  sequence: []
  outputs:
    artifact_refs:
      empty.txt: {kind: value, format: text, value: ""}
`
	source := filepath.Join(root, "recipe.yaml")
	suite := filepath.Join(root, "suite.test.yaml")
	if err := os.WriteFile(source, []byte(recipe), 0600); err != nil {
		t.Fatal(err)
	}
	declaration := `recipe: recipe.yaml
cases:
- id: empty
  type: integration_case
  runtime:
    observe:
      scopes:
      - node_path: empty/verify
        outputs: [artifact_refs]
        retain_artifacts: [empty.txt]
`
	if err := os.WriteFile(suite, []byte(declaration), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "report")
	if err := Run(context.Background(), Options{FilePath: suite, WorkingDir: root, OutDir: out, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out, "cases", "empty", "artifacts", "observations", "observation-1", fmt.Sprintf("artifact-%x", sha256.Sum256([]byte("empty.txt")))))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatal("expected empty diagnostic")
	}
}

// OBS-015/021: metadata redaction must preserve distinct, retrievable files.
func TestNativeObservationArtifactMetadataRedaction(t *testing.T) {
	for _, publicCollision := range []bool{false, true} {
		t.Run(fmt.Sprintf("public-collision-%t", publicCollision), func(t *testing.T) {
			testNativeObservationArtifactMetadataRedaction(t, publicCollision)
		})
	}
}

func testNativeObservationArtifactMetadataRedaction(t *testing.T, publicCollision bool) {
	root, err := filepath.Abs("../../../../testdata/observations")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	recipe, err := os.ReadFile(filepath.Join(root, "bridge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	suite, err := os.ReadFile(filepath.Join(root, "bridge.test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"fixture-secret.txt", "fixture-other.txt"}
	recipe = bytes.ReplaceAll(recipe, []byte("diagnostic.txt: {kind: value, format: text, value: \"diagnostic fixture-secret\"}"), []byte("fixture-secret.txt: {kind: value, format: text, value: \"first fixture-secret\"}\n              fixture-other.txt: {kind: value, format: text, value: \"second fixture-other\"}"))
	suite = bytes.ReplaceAll(suite, []byte("secrets: {token: fixture-secret}"), []byte("secrets: {token: fixture-secret, other: fixture-other}"))
	suite = bytes.ReplaceAll(suite, []byte("retain_artifacts: [diagnostic.txt]"), []byte("retain_artifacts: [fixture-secret.txt, fixture-other.txt]"))
	suite = bytes.ReplaceAll(suite, []byte("  - type: artifact_exists\n    path: observations/observation-1/artifact-13707d0f731502acdd6e65893763aef03c8258d15cb37c2d64e1ba0475765635"), nil)
	if publicCollision {
		publicName := fmt.Sprintf("artifact-%x", sha256.Sum256([]byte(names[0])))
		recipe = bytes.ReplaceAll(recipe, []byte("fixture-other.txt"), []byte(publicName))
		recipe = bytes.ReplaceAll(recipe, []byte("second fixture-other"), []byte("second public diagnostic"))
		suite = bytes.ReplaceAll(suite, []byte("fixture-other.txt"), []byte(publicName))
		names[1] = publicName
	}
	for _, name := range names {
		path := fmt.Sprintf("observations/observation-1/artifact-%x", sha256.Sum256([]byte(name)))
		suite = append(suite, []byte("  - type: artifact_exists\n    path: "+path+"\n")...)
	}
	for name, data := range map[string][]byte{"bridge.yaml": recipe, "bridge.test.yaml": suite} {
		if err := os.WriteFile(filepath.Join(work, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "report")
	if err := Run(context.Background(), Options{FilePath: filepath.Join(work, "bridge.test.yaml"), WorkingDir: work, OutDir: out, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	// Only persisted report files remain available for retrieval.
	if err := os.RemoveAll(work); err != nil {
		t.Fatal(err)
	}
	caseDir := filepath.Join(out, "cases", "bounded-observation")
	report, err := os.ReadFile(filepath.Join(caseDir, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Run struct {
			Runtime struct {
				Artifacts []struct {
					Name, Path, Status string
					Size               int `json:"size_bytes"`
				} `json:"observation_artifacts"`
				Collection struct{ Complete bool } `json:"observation_collection"`
			} `json:"runtime"`
		} `json:"run"`
	}
	if err := json.Unmarshal(report, &decoded); err != nil {
		t.Fatal(err)
	}
	manifests := decoded.Run.Runtime.Artifacts
	if !decoded.Run.Runtime.Collection.Complete || len(manifests) != 2 || manifests[0].Path == manifests[1].Path {
		t.Fatalf("distinct manifests required: %s", report)
	}
	for i, m := range manifests {
		displayName := "[REDACTED].txt"
		if publicCollision && i == 1 {
			displayName = names[1]
		}
		if m.Name != displayName || m.Status != "retained" {
			t.Fatalf("unexpected manifest: %+v", m)
		}
		data, err := os.ReadFile(filepath.Join(caseDir, m.Path))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"first [REDACTED]", "second [REDACTED]"}[i]
		if publicCollision && i == 1 {
			want = "second public diagnostic"
		}
		if string(data) != want || len(data) != m.Size {
			t.Fatalf("got %q want %q", data, want)
		}
	}
	if err := filepath.WalkDir(out, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		for _, secret := range []string{"fixture-secret", "fixture-other"} {
			if strings.Contains(path, secret) {
				t.Fatalf("secret in path: %s", path)
			}
			if !d.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if bytes.Contains(data, []byte(secret)) {
					t.Fatalf("secret in persisted report: %s", path)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
