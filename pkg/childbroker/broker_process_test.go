package childbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
)

const workerBrokerChildRecipeYAML = `
id: child_from_worker
version: "1.0.0"
sequence:
  - id: child_group_marker
    child_group:
      mode: start
      children: []
outputs: {}
`

func TestWorkerBrokerChildRecipeFixtureRoundTripsThroughSubmitRequest(t *testing.T) {
	rec, err := recipe.LoadRecipeFromReader(strings.NewReader(workerBrokerChildRecipeYAML))
	if err != nil {
		t.Fatalf("load fixture recipe: %v", err)
	}
	req, err := NewSubmitRequest(context.Background(), workflowctl.StartJob{
		TenantId:   "0",
		RecipeName: "child_from_worker",
	}, nil, *rec)
	if err != nil {
		t.Fatalf("NewSubmitRequest(): %v", err)
	}
	if len(req.EmbeddedRecipes) != 1 {
		t.Fatalf("embedded recipes = %d, want 1", len(req.EmbeddedRecipes))
	}
	if _, err := recipe.LoadRecipeFromReader(bytes.NewReader(req.EmbeddedRecipes[0].YAML)); err != nil {
		t.Fatalf("broker decode of embedded fixture recipe failed after submit-request marshal:\n%s\nerror: %v", string(req.EmbeddedRecipes[0].YAML), err)
	}
}

func TestBrokerSubmitFromC2JInWorkerEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	repoRoot := testRepoRoot(t)
	binDir := t.TempDir()
	binaryName := "c2j"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	c2jPath := filepath.Join(binDir, binaryName)
	build := exec.CommandContext(ctx, "go", "build", "-o", c2jPath, "./cmd/c2j")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build c2j: %v\n%s", err, string(out))
	}
	if err := os.Chmod(c2jPath, 0o755); err != nil {
		t.Fatalf("chmod c2j: %v", err)
	}

	workspace := t.TempDir()
	writeWorkerBrokerWorkspace(t, workspace)

	current := jobcontext.Current{
		TenantID:           "0",
		JobID:              "parent-worker",
		JobType:            starter.RecipeJobType,
		OpType:             "command_execution",
		OpStep:             "submit-child",
		OpTaskType:         "activity:command_execution",
		CellName:           "parent-cell",
		RepositorySource:   "file:///parent",
		GitRef:             "main",
		InvocationPath:     "sequence.submit-child",
		InvocationSequence: 7,
		InvocationHash:     "invoke-worker",
	}
	submitter := &captureSubmitter{}
	broker, err := Start(ctx, Options{
		Current:   current,
		Submitter: submitter,
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer broker.Close()

	env := jobcontext.EnvForCurrent(current)
	for key, value := range broker.Env() {
		env[key] = value
	}

	stdout, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{
		WorkspaceRoot: workspace,
		WorkingDir:    workspace,
		Command:       []string{c2jPath, "submit", "Run the child broker fixture", "--embed", "--cell", workspace, "--advanced-recipe-file", "child.yaml", "--json"},
		Env:           env,
	})
	if err != nil {
		t.Fatalf("run c2j submit in worker environment: %v\nstdout:\n%s\nstderr:\n%s", err, string(stdout), string(stderr))
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(stdout))), &submitted); err != nil {
		t.Fatalf("decode c2j submit output %q: %v\nstderr:\n%s", string(stdout), err, string(stderr))
	}
	if submitted.TenantID != "0" || submitted.JobID == "" || submitted.Recipe != "child_from_worker" {
		t.Fatalf("unexpected c2j submit output: %#v", submitted)
	}
	if submitter.calls != 1 {
		t.Fatalf("submit calls = %d, want 1", submitter.calls)
	}
	if submitter.last.TenantId != "0" || submitter.lastKey.JobId != submitted.JobID {
		t.Fatalf("unexpected submitted job: %#v", submitter.last)
	}

	artifacts, err := submitter.last.Data.GetArtifacts()
	if err != nil {
		t.Fatalf("GetArtifacts(): %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Name() != "child_from_worker.recipe.yaml" {
		t.Fatalf("unexpected submitted artifacts: %#v", artifacts)
	}

	var meta starter.JobMetadata
	if err := json.Unmarshal(submitter.last.Metadata, &meta); err != nil {
		t.Fatalf("metadata decode: %v", err)
	}
	if meta.ParentTenantID != "0" || meta.ParentJobID != "parent-worker" || meta.ParentInvocationHash != "invoke-worker" {
		t.Fatalf("broker did not attach parent metadata: %#v", meta)
	}
	if meta.ParentOpStep != "submit-child" || meta.ParentOpType != "command_execution" {
		t.Fatalf("broker did not preserve parent op context: %#v", meta)
	}

	started := broker.StartedJobs()
	if len(started.JobIDs) != 1 || started.JobIDs[0] != submitted.JobID {
		t.Fatalf("unexpected broker started jobs: %#v", started)
	}
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}

func writeWorkerBrokerWorkspace(t *testing.T, workspace string) {
	t.Helper()
	configPath := filepath.Join(workspace, ".shai", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir .shai: %v", err)
	}
	config := `[invalid: yaml`
	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(config)+"\n"), 0o644); err != nil {
		t.Fatalf("write shai config: %v", err)
	}

	if err := os.WriteFile(filepath.Join(workspace, "child.yaml"), []byte(strings.TrimSpace(workerBrokerChildRecipeYAML)+"\n"), 0o644); err != nil {
		t.Fatalf("write child recipe: %v", err)
	}
}
