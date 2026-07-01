package submitjob

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"net/http/httptest"
)

func testJobDBURI(serverURL string, tenantID string) string {
	return strings.TrimRight(serverURL, "/") + "/" + tenantID
}

func TestRun_SubmitsJobThatCanBeRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-test"
	recipeYAML := strings.TrimSpace(`
id: nucleus_submit_recipe
desc: simple recipe used to verify c2j submission
version: "1.0"
sequence:
  - id: echo
    op: command_execution
    inputs:
      run: "echo hello-from-submit"
      working_directory: "."
outputs:
  result: "{{ sequence.echo.outputs.stdout }}"
`) + "\n"

	recipePath := filepath.Join(t.TempDir(), "nucleus_submit_recipe.yaml")
	if err := os.WriteFile(recipePath, []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	baseRepo, _ := createGitRepo(t)

	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:   testJobDBURI(server.URL, tenantID),
		RecipeFile: recipePath,
		Cell:       baseRepo,
		JSONOutput: true,
		Stdout:     &submitStdout,
	}); err != nil {
		t.Fatalf("submit job: %v", err)
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal(submitStdout.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit output: %v", err)
	}
	if submitted.JobID == "" {
		t.Fatalf("expected job id in submit output: %s", submitStdout.String())
	}

	var runStdout bytes.Buffer
	var runStderr bytes.Buffer
	if err := runjob.Run(ctx, runjob.Options{
		JobID:        submitted.JobID,
		JobDBURI:     testJobDBURI(server.URL, tenantID),
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &runStdout,
		Stderr:       &runStderr,
	}); err != nil {
		t.Fatalf("run submitted job: %v\nstderr:\n%s", err, runStderr.String())
	}

	runtime, err := remoteruntime.New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create remote runtime: %v", err)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobdb.JobKey{TenantId: tenantID, JobId: submitted.JobID},
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	output, err := run.GetOutput(engine, tenantID)
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	raw, err := output.GetData()
	if err != nil {
		t.Fatalf("get output data: %v", err)
	}

	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got["result"] != "hello-from-submit" {
		t.Fatalf("unexpected output: %#v", got)
	}
	if strings.Contains(runStderr.String(), "warning: replay unavailable") {
		t.Fatalf("unexpected replay warning:\n%s", runStderr.String())
	}
}

func TestRun_SubmitsAttachedArtifactAndRecipeReadsInboxFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-artifact-test"
	recipeYAML := strings.TrimSpace(`
id: submit_artifact_recipe
desc: verifies submit-time file artifacts are visible to recipe ops
version: "1.0"
sequence:
  - id: read_doc
    op: command_execution
    artifacts:
      brief.md: '${{ context.artifacts["brief.md"] }}'
    inputs:
      run: 'cat "${{ context.environment.op.inbox }}/brief.md"'
      working_directory: "."
outputs:
  result: "{{ sequence.read_doc.outputs.stdout }}"
`) + "\n"

	root := t.TempDir()
	recipePath := filepath.Join(root, "submit_artifact_recipe.yaml")
	if err := os.WriteFile(recipePath, []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}
	briefPath := filepath.Join(root, "brief.md")
	if err := os.WriteFile(briefPath, []byte("hello from attached markdown"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}

	baseRepo, _ := createGitRepo(t)

	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:      testJobDBURI(server.URL, tenantID),
		RecipeFile:    recipePath,
		Cell:          baseRepo,
		WorkingDir:    root,
		ArtifactSpecs: []string{"brief.md"},
		JSONOutput:    true,
		Stdout:        &submitStdout,
	}); err != nil {
		t.Fatalf("submit job: %v", err)
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal(submitStdout.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit output: %v", err)
	}

	var runStdout bytes.Buffer
	var runStderr bytes.Buffer
	if err := runjob.Run(ctx, runjob.Options{
		JobID:        submitted.JobID,
		JobDBURI:     testJobDBURI(server.URL, tenantID),
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &runStdout,
		Stderr:       &runStderr,
	}); err != nil {
		t.Fatalf("run submitted job: %v\nstderr:\n%s", err, runStderr.String())
	}

	runtime, err := remoteruntime.New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create remote runtime: %v", err)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobdb.JobKey{TenantId: tenantID, JobId: submitted.JobID},
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	output, err := run.GetOutput(engine, tenantID)
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	raw, err := output.GetData()
	if err != nil {
		t.Fatalf("get output data: %v", err)
	}

	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got["result"] != "hello from attached markdown" {
		t.Fatalf("unexpected output: %#v", got)
	}
}

func TestRun_ForwardsSubmittedArtifactToChildRecipe(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tenantID := "tenant-child-artifact-forwarding-test"
	parentYAML := strings.TrimSpace(`
id: parent-artifact-forwarding
version: "1.0"
sequence:
  - id: child
    op: recipe.run_and_get_result
    inputs:
      name: child-artifact-forwarding
      inputs: {}
      artifacts: '${{ context.artifacts.map(k, context.artifacts[k]) }}'
outputs:
  child_received: "${{ sequence.child.outputs.outputs.received }}"
`) + "\n"
	childYAML := strings.TrimSpace(`
id: child-artifact-forwarding
version: "1.0"
sequence:
  - id: verify
    op: command_execution
    artifacts:
      submitted/: '${{ context.artifacts }}'
    inputs:
      run: |
        set -euo pipefail
        test -f "{{ context.environment.op.inbox }}/submitted/brief.md"
        grep -q "child-artifact-forwarding-ok" "{{ context.environment.op.inbox }}/submitted/brief.md"
      working_directory: "."
outputs:
  received: true
`) + "\n"

	baseRepo, _ := createGitRepo(t)
	mustWriteRepoFile(t, baseRepo, ".c2j/recipes/parent-artifact-forwarding.yaml", parentYAML)
	mustWriteRepoFile(t, baseRepo, ".c2j/recipes/child-artifact-forwarding.yaml", childYAML)
	commitRepo(t, baseRepo, "add artifact forwarding recipes")

	root := t.TempDir()
	briefPath := filepath.Join(root, "brief.md")
	if err := os.WriteFile(briefPath, []byte("child-artifact-forwarding-ok\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}

	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:      testJobDBURI(server.URL, tenantID),
		Recipe:        "parent-artifact-forwarding",
		Cell:          baseRepo,
		WorkingDir:    baseRepo,
		ArtifactSpecs: []string{"brief.md=" + briefPath},
		JSONOutput:    true,
		Stdout:        &submitStdout,
	}); err != nil {
		t.Fatalf("submit parent job: %v", err)
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal(submitStdout.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit output: %v", err)
	}

	var firstParentStdout bytes.Buffer
	var firstParentStderr bytes.Buffer
	err := runjob.Run(ctx, runjob.Options{
		JobID:        submitted.JobID,
		JobDBURI:     testJobDBURI(server.URL, tenantID),
		OnNotReady:   "fail-on-pending-jobs",
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &firstParentStdout,
		Stderr:       &firstParentStderr,
	})
	if err == nil {
		t.Fatal("expected parent to pause waiting for child job")
	}
	childJobID := extractWaitForJobID(t, err)

	var childStdout bytes.Buffer
	var childStderr bytes.Buffer
	if err := runjob.Run(ctx, runjob.Options{
		JobID:        childJobID,
		JobDBURI:     testJobDBURI(server.URL, tenantID),
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &childStdout,
		Stderr:       &childStderr,
	}); err != nil {
		t.Fatalf("run child job: %v\nstderr:\n%s", err, childStderr.String())
	}

	var finalParentStdout bytes.Buffer
	var finalParentStderr bytes.Buffer
	if err := runjob.Run(ctx, runjob.Options{
		JobID:        submitted.JobID,
		JobDBURI:     testJobDBURI(server.URL, tenantID),
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &finalParentStdout,
		Stderr:       &finalParentStderr,
	}); err != nil {
		t.Fatalf("resume parent job: %v\nstderr:\n%s", err, finalParentStderr.String())
	}

	runtime, err := remoteruntime.New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create remote runtime: %v", err)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobdb.JobKey{TenantId: tenantID, JobId: submitted.JobID},
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get parent job run: %v", err)
	}
	output, err := run.GetOutput(engine, tenantID)
	if err != nil {
		t.Fatalf("get parent output: %v", err)
	}
	raw, err := output.GetData()
	if err != nil {
		t.Fatalf("get parent output data: %v", err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode parent output: %v", err)
	}
	if got["child_received"] != true {
		t.Fatalf("unexpected parent output: %#v", got)
	}
}

func TestRun_SubmitsRecipeReferenceThatResolvesAtExecution(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-ref-test"
	recipeYAML := strings.TrimSpace(`
id: nucleus_submit_ref_recipe
desc: simple recipe used to verify c2j reference submission
version: "1.0"
sequence:
  - id: echo
    op: command_execution
    inputs:
      run: "echo hello-from-submit-ref"
      working_directory: "."
outputs:
  result: "{{ sequence.echo.outputs.stdout }}"
`) + "\n"

	baseRepo, _ := createGitRepo(t)
	mustWriteRepoFile(t, baseRepo, ".c2j/recipes/nucleus_submit_ref_recipe.yaml", recipeYAML)
	commitRepo(t, baseRepo, "add bare recipe")

	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:   testJobDBURI(server.URL, tenantID),
		Recipe:     "nucleus_submit_ref_recipe",
		Cell:       baseRepo,
		JSONOutput: true,
		Stdout:     &submitStdout,
	}); err != nil {
		t.Fatalf("submit job: %v", err)
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal(submitStdout.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit output: %v", err)
	}
	if submitted.Recipe != "nucleus_submit_ref_recipe" {
		t.Fatalf("submitted recipe = %q", submitted.Recipe)
	}

	var runStdout bytes.Buffer
	var runStderr bytes.Buffer
	if err := runjob.Run(ctx, runjob.Options{
		JobID:        submitted.JobID,
		JobDBURI:     testJobDBURI(server.URL, tenantID),
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &runStdout,
		Stderr:       &runStderr,
	}); err != nil {
		t.Fatalf("run submitted job: %v\nstderr:\n%s", err, runStderr.String())
	}

	runtime, err := remoteruntime.New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create remote runtime: %v", err)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobdb.JobKey{TenantId: tenantID, JobId: submitted.JobID},
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	if len(run.Attempts) == 0 || len(run.Attempts[0].Tasks) == 0 {
		t.Fatalf("expected recorded tasks in job run: %#v", run.Attempts)
	}
	if run.Attempts[0].Tasks[0].TaskType != compiler.RootSourceResolutionTaskType {
		t.Fatalf("expected first task %q, got %q", compiler.RootSourceResolutionTaskType, run.Attempts[0].Tasks[0].TaskType)
	}
}

func TestRun_SubmitsCurrentCellByDefaultUsingConfigDefaultRecipe(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-self-test"
	recipeYAML := strings.TrimSpace(`
id: default_recipe
desc: simple recipe used to verify c2j self submission
version: "1.0"
sequence:
  - id: echo
    op: command_execution
    inputs:
      run: "echo hello-from-self"
      working_directory: "."
outputs:
  result: "{{ sequence.echo.outputs.stdout }}"
`) + "\n"

	baseRepo, _ := createGitRepo(t)
	mustWriteRepoFile(t, baseRepo, ".c2j/recipes/default.yaml", recipeYAML)
	mustWriteRepoFile(t, baseRepo, ".c2j/config.yaml", "self:\n  repo: "+baseRepo+"\n  ref: main\n")
	commitRepo(t, baseRepo, "add self config and default recipe")

	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:   testJobDBURI(server.URL, tenantID),
		WorkingDir: baseRepo,
		JSONOutput: true,
		Stdout:     &submitStdout,
	}); err != nil {
		t.Fatalf("submit job: %v", err)
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal(submitStdout.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit output: %v", err)
	}

	var runStdout bytes.Buffer
	var runStderr bytes.Buffer
	if err := runjob.Run(ctx, runjob.Options{
		JobID:        submitted.JobID,
		JobDBURI:     testJobDBURI(server.URL, tenantID),
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &runStdout,
		Stderr:       &runStderr,
	}); err != nil {
		t.Fatalf("run submitted job: %v\nstderr:\n%s", err, runStderr.String())
	}

	runtime, err := remoteruntime.New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create remote runtime: %v", err)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobdb.JobKey{TenantId: tenantID, JobId: submitted.JobID},
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	output, err := run.GetOutput(engine, tenantID)
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	raw, err := output.GetData()
	if err != nil {
		t.Fatalf("get output data: %v", err)
	}

	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got["result"] != "hello-from-self" {
		t.Fatalf("unexpected output: %#v", got)
	}
}

func TestRun_SubmitsPromptAndRunsJobImmediately(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-prompt-test"
	recipeYAML := strings.TrimSpace(`
id: prompt_submit_recipe
desc: verifies positional prompt input and immediate execution
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
inputs:
  prompt: "{{ inputs.prompt }}"
sequence: []
outputs:
  received_prompt: "{{ inputs.prompt }}"
`) + "\n"

	recipePath := filepath.Join(t.TempDir(), "prompt_submit_recipe.yaml")
	if err := os.WriteFile(recipePath, []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	baseRepo, _ := createGitRepo(t)

	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	var submitStdout bytes.Buffer
	var submitStderr bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:       testJobDBURI(server.URL, tenantID),
		RecipeFile:     recipePath,
		Cell:           baseRepo,
		Prompt:         "hello from positional prompt",
		PromptSet:      true,
		RunAfterSubmit: true,
		Stdin:          bytes.NewBuffer(nil),
		Stdout:         &submitStdout,
		Stderr:         &submitStderr,
	}); err != nil {
		t.Fatalf("submit and run job: %v\nstderr:\n%s", err, submitStderr.String())
	}

	firstLine, _, _ := strings.Cut(submitStdout.String(), "\n")
	jobID := extractSubmittedJobID(firstLine)
	if jobID == "" {
		t.Fatalf("expected job id in submit output: %q", submitStdout.String())
	}

	runtime, err := remoteruntime.New(server.URL, server.Client())
	if err != nil {
		t.Fatalf("create remote runtime: %v", err)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}

	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobdb.JobKey{TenantId: tenantID, JobId: jobID},
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	output, err := run.GetOutput(engine, tenantID)
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	raw, err := output.GetData()
	if err != nil {
		t.Fatalf("get output data: %v", err)
	}

	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got["received_prompt"] != "hello from positional prompt" {
		t.Fatalf("unexpected output: %#v", got)
	}
}

func TestRun_SubmitsAndExecutesWithEmbeddedRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Setenv("HOME", t.TempDir())

	recipeYAML := strings.TrimSpace(`
id: embed_submit_recipe
desc: simple recipe used to verify c2j embedded execution
version: "1.0"
sequence:
  - id: echo
    op: command_execution
    inputs:
      run: "echo hello-from-embed"
      working_directory: "."
outputs:
  result: "{{ sequence.echo.outputs.stdout }}"
`) + "\n"

	recipePath := filepath.Join(t.TempDir(), "embed_submit_recipe.yaml")
	if err := os.WriteFile(recipePath, []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	baseRepo, _ := createGitRepo(t)

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:   "embed:///",
		RecipeFile: recipePath,
		Cell:       baseRepo,
		JSONOutput: true,
		Stdout:     &submitStdout,
		Stderr:     &bytes.Buffer{},
	}); err != nil {
		t.Fatalf("submit job: %v", err)
	}

	var submitted struct {
		TenantID string `json:"tenant_id"`
		JobID    string `json:"job_id"`
		Recipe   string `json:"recipe"`
	}
	if err := json.Unmarshal(submitStdout.Bytes(), &submitted); err != nil {
		t.Fatalf("decode submit output: %v", err)
	}
	if submitted.JobID == "" {
		t.Fatalf("expected job id in submit output: %s", submitStdout.String())
	}
	if submitted.TenantID != defaults.EmbeddedTenantID {
		t.Fatalf("submitted tenant = %q, want embedded tenant %q", submitted.TenantID, defaults.EmbeddedTenantID)
	}

	var runStdout bytes.Buffer
	var runStderr bytes.Buffer
	if err := runjob.Run(ctx, runjob.Options{
		JobID:        submitted.JobID,
		JobDBURI:     "embed:///",
		WaitTimeout:  15 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &runStdout,
		Stderr:       &runStderr,
	}); err != nil {
		t.Fatalf("run submitted job: %v\nstderr:\n%s", err, runStderr.String())
	}

	handle, err := swfruntime.Open(ctx, "embed:///")
	if err != nil {
		t.Fatalf("Open(embed): %v", err)
	}
	defer handle.Cleanup()

	run, err := handle.Engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobdb.JobKey{TenantId: defaults.EmbeddedTenantID, JobId: submitted.JobID},
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	output, err := run.GetOutput(handle.Engine, defaults.EmbeddedTenantID)
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	raw, err := output.GetData()
	if err != nil {
		t.Fatalf("get output data: %v", err)
	}

	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got["result"] != "hello-from-embed" {
		t.Fatalf("unexpected output: %#v", got)
	}
}

func TestRun_EmbedExtensionFailureCompletesWithOriginalError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Setenv("HOME", t.TempDir())

	baseRepo, _ := createGitRepo(t)
	mustWriteRepoFile(t, baseRepo, ".c2j/recipes/failing_extension_recipe.yaml", strings.TrimSpace(`
id: failing_extension_recipe
desc: verifies extension failures become terminal recipe failures
version: "1.0"
sequence:
  - id: fail
    op: ./tools/ops/fail
    inputs: {}
outputs:
  ok: "{{ sequence.fail.outputs.ok }}"
`)+"\n")
	mustWriteRepoFile(t, baseRepo, "tools/ops/fail/op.yaml", strings.TrimSpace(`
name: fail
shell: sh
run: |
  echo extension failure >&2
  exit 23
input_schema:
  type: object
  properties: {}
output_schema:
  type: object
  properties:
    ok:
      type: boolean
`)+"\n")
	commitRepo(t, baseRepo, "add failing extension recipe")

	var submitStdout bytes.Buffer
	var submitStderr bytes.Buffer
	err := Run(ctx, Options{
		JobDBURI:       "embed:///",
		Recipe:         "failing_extension_recipe",
		Cell:           baseRepo,
		RunAfterSubmit: true,
		Stdin:          bytes.NewBuffer(nil),
		Stdout:         &submitStdout,
		Stderr:         &submitStderr,
	})
	if err == nil {
		t.Fatal("expected submit --run to fail")
	}
	errText := err.Error()
	if !strings.Contains(errText, "extension op") || !strings.Contains(errText, "extension failure") {
		t.Fatalf("expected original extension failure, got %v\nstderr:\n%s", err, submitStderr.String())
	}
	if strings.Contains(errText, "workflow state conflict") || strings.Contains(errText, "chapter ordinal") {
		t.Fatalf("chapter conflict masked extension failure: %v\nstderr:\n%s", err, submitStderr.String())
	}

	firstLine, _, _ := strings.Cut(submitStdout.String(), "\n")
	jobID := extractSubmittedJobID(firstLine)
	if jobID == "" {
		t.Fatalf("expected job id in submit output: %q", submitStdout.String())
	}

	handle, err := swfruntime.Open(ctx, "embed:///")
	if err != nil {
		t.Fatalf("Open(embed): %v", err)
	}
	defer handle.Cleanup()

	jobKey := jobdb.JobKey{TenantId: defaults.EmbeddedTenantID, JobId: jobID}
	info, err := handle.Engine.GetJob(ctx, jobKey)
	if err != nil {
		t.Fatalf("GetJob(): %v", err)
	}
	if info.Status != jobdb.JobStatusCompleted {
		t.Fatalf("job status = %s, want %s", info.Status, jobdb.JobStatusCompleted)
	}

	run, err := handle.Engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	if _, err := run.GetOutput(handle.Engine, defaults.EmbeddedTenantID); err == nil {
		t.Fatal("expected failed job output")
	} else if !strings.Contains(err.Error(), "extension failure") {
		t.Fatalf("expected job output error to preserve extension failure, got %v", err)
	}
}

func extractSubmittedJobID(line string) string {
	for _, field := range strings.Fields(line) {
		if value, ok := strings.CutPrefix(field, "job_id="); ok {
			return value
		}
	}
	return ""
}

func extractWaitForJobID(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected error containing wait_for job id")
	}
	const marker = "wait_for="
	message := err.Error()
	idx := strings.Index(message, marker)
	if idx < 0 {
		t.Fatalf("expected wait_for job id in error %q", message)
	}
	rest := message[idx+len(marker):]
	if fields := strings.Fields(rest); len(fields) > 0 {
		rest = fields[0]
	}
	if comma := strings.Index(rest, ","); comma >= 0 {
		rest = rest[:comma]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		t.Fatalf("expected non-empty wait_for job id in error %q", message)
	}
	return rest
}

func createGitRepo(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}

	run("git", "init", "-b", "main")
	run("git", "config", "user.email", "c2j-test@example.com")
	run("git", "config", "user.name", "Nucleus Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("c2j\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	return dir, run("git", "rev-parse", "HEAD")
}

func mustWriteRepoFile(t *testing.T, repoDir string, relativePath string, contents string) {
	t.Helper()

	fullPath := filepath.Join(repoDir, relativePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(fullPath, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
}

func commitRepo(t *testing.T, repoDir string, message string) {
	t.Helper()

	cmd := exec.Command("git", "add", ".")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}

	cmd = exec.Command("git", "commit", "-m", message)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}
}
