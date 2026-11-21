package submitjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/cellref"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/runjob"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/parentagetest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/swfutil"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func testJobDBURI(serverURL string, tenantID string) string {
	return strings.TrimRight(serverURL, "/") + "/" + tenantID
}

func TestRun_SubmitsJobThatCanBeRun(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-test"
	recipeYAML := strings.TrimSpace(`
id: nucleus_submit_recipe
input_schema:
  prompt:
    type: string
  title:
    type: string
desc: simple recipe used to verify c2j submission
version: "1.0"
inputs:
  title: "${{ inputs.title }}"
sequence:
  - id: echo
    op: command_execution
    inputs:
      run: "echo hello-from-submit"
      working_directory: "."
outputs:
  result: "{{ sequence.echo.outputs.stdout }}"
  title: "{{ inputs.title }}"
`) + "\n"

	recipePath := filepath.Join(t.TempDir(), "nucleus_submit_recipe.yaml")
	if err := os.WriteFile(recipePath, []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	baseRepo, _ := createGitRepo(t)

	underlying := toyruntime.New()
	celltest.Create(t, underlying, tenantID, "app", celltest.Repo(baseRepo))
	server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(underlying)))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:   testJobDBURI(server.URL, tenantID),
		RecipeFile: recipePath,
		Cell:       "app", CellRepo: celltest.Repo(baseRepo),
		JSONOutput: true,
		Prompt:     "run the test recipe",
		Title:      "  Test recipe title  ",
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
	if got["title"] != "  Test recipe title  " {
		t.Fatalf("title was not preserved in recipe inputs: %#v", got)
	}
	if strings.Contains(runStderr.String(), "warning: replay unavailable") {
		t.Fatalf("unexpected replay warning:\n%s", runStderr.String())
	}
}

func TestRun_SubmitsAttachedArtifactAndRecipeReadsInboxFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-artifact-test"
	recipeYAML := strings.TrimSpace(`
id: submit_artifact_recipe
input_schema:
  prompt:
    type: string
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
	celltest.Create(t, underlying, tenantID, "app", celltest.Repo(baseRepo))
	server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(underlying)))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:   testJobDBURI(server.URL, tenantID),
		RecipeFile: recipePath,
		Cell:       "app", CellRepo: celltest.Repo(baseRepo),
		WorkingDir:    root,
		ArtifactSpecs: []string{"brief.md"},
		Prompt:        "read the attached brief",
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

	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
	defer cancel()

	tenantID := "tenant-child-artifact-forwarding-test"
	parentYAML := strings.TrimSpace(`
id: parent-artifact-forwarding
input_schema:
  prompt:
    type: string
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
	mustWriteRepoFile(t, baseRepo, ".c2/recipes/parent-artifact-forwarding.yaml", parentYAML)
	mustWriteRepoFile(t, baseRepo, ".c2/recipes/child-artifact-forwarding.yaml", childYAML)
	commitRepo(t, baseRepo, "add artifact forwarding recipes")

	root := t.TempDir()
	briefPath := filepath.Join(root, "brief.md")
	if err := os.WriteFile(briefPath, []byte("child-artifact-forwarding-ok\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}

	underlying := toyruntime.New()
	celltest.Create(t, underlying, tenantID, "app", celltest.Repo(baseRepo))
	server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(underlying)))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI: testJobDBURI(server.URL, tenantID),
		Recipe:   "parent-artifact-forwarding",
		Prompt:   "forward the attached brief",
		Cell:     "app", CellRepo: celltest.Repo(baseRepo),
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
	discoveryRuntime, openErr := remoteruntime.New(server.URL, server.Client())
	if openErr != nil {
		t.Fatal(openErr)
	}
	children, listErr := discoveryRuntime.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{tenantID}, ParentJobIDs: []string{submitted.JobID}})
	if listErr != nil || len(children.Jobs) != 1 {
		t.Fatalf("discover submitted child: %v, %#v", listErr, children)
	}
	childJobID := children.Jobs[0].JobKey.JobId

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

	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-ref-test"
	recipeYAML := strings.TrimSpace(`
id: nucleus_submit_ref_recipe
input_schema:
  prompt:
    type: string
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
	mustWriteRepoFile(t, baseRepo, ".c2/recipes/nucleus_submit_ref_recipe.yaml", recipeYAML)
	commitRepo(t, baseRepo, "add bare recipe")

	underlying := toyruntime.New()
	celltest.Create(t, underlying, tenantID, "app", celltest.Repo(baseRepo))
	server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(underlying)))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI: testJobDBURI(server.URL, tenantID),
		Recipe:   "nucleus_submit_ref_recipe",
		Prompt:   "run the referenced recipe",
		Cell:     "app", CellRepo: celltest.Repo(baseRepo),
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
	tasks, err := swfutil.ExecutionTasks(run)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected recorded tasks in execution history")
	}
	if len(tasks) < 2 || tasks[0].TaskType != compiler.CellContextTaskType || tasks[1].TaskType != compiler.RootSourceResolutionTaskType {
		t.Fatalf("expected first task %q, got %q", compiler.RootSourceResolutionTaskType, tasks[0].TaskType)
	}
}

func TestRun_DefaultBuildUsesSharedRecipe(t *testing.T) {
	t.Parallel()
	for _, privateTarget := range []bool{false, true} {
		t.Run(fmt.Sprintf("private=%v", privateTarget), func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			root, _ := createGitRepo(t)
			mustWriteRepoFile(t, root, ".c2/recipes/build.yaml", "invalid: [")
			mustWriteRepoFile(t, root, ".c2/config.yaml", "self:\n  cell: app\n  repo: "+celltest.Repo(root)+"\n  ref: main\n")
			commitRepo(t, root, "local recipe must not override default")
			base := toyruntime.New()
			server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(base)))
			defer server.Close()
			var stdout bytes.Buffer
			opts := Options{JobDBURI: testJobDBURI(server.URL, "default-build"), WorkingDir: root, Prompt: "build it", JSONOutput: true, Stdout: &stdout}
			if privateTarget {
				opts.Cell = "private"
				opts.CellRepo = "https://private.invalid/team/repo.git"
			}
			if privateTarget {
				celltest.Create(t, base, "default-build", opts.Cell, opts.CellRepo)
			} else {
				celltest.Create(t, base, "default-build", "app", celltest.Repo(root))
			}
			if err := Run(ctx, opts); err != nil {
				t.Fatal(err)
			}
			var submitted submitResult
			if err := json.Unmarshal(stdout.Bytes(), &submitted); err != nil {
				t.Fatal(err)
			}
			if submitted.Recipe != compiler.SharedBuildRecipeSelector {
				t.Fatalf("recipe = %q", submitted.Recipe)
			}
			run, err := jobdb.GetJobRun(ctx, base, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: submitted.TenantID, JobId: submitted.JobID}, IncludeInputs: true})
			if err != nil {
				t.Fatal(err)
			}
			if run.Start.Input == nil {
				t.Fatal("missing submitted inputs")
			}
			var start workflowctl.StartJob
			if err := json.Unmarshal(run.Start.Input.Data, &start); err != nil {
				t.Fatal(err)
			}
			if start.RecipeName != compiler.SharedBuildRecipeSelector || start.Inputs["type"] != "build" || start.Inputs["prompt"] != "build it" {
				t.Fatalf("unexpected submitted inputs: %+v", start)
			}
			wantRepo := "https://" + celltest.Repo(root) + ".git"
			if privateTarget {
				wantRepo = opts.CellRepo
			}
			if start.JobContext.GitBase.BaseRepo != wantRepo {
				t.Fatalf("target = %q, want %q", start.JobContext.GitBase.BaseRepo, wantRepo)
			}
		})
	}
}

func TestRun_SubmitsCurrentCellUsingExplicitBuildRecipe(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
	defer cancel()

	tenantID := "tenant-submit-self-test"
	recipeYAML := strings.TrimSpace(`
id: build_recipe
input_schema:
  prompt:
    type: string
  type:
    type: string
    required: true
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
	mustWriteRepoFile(t, baseRepo, ".c2/recipes/build.yaml", recipeYAML)
	mustWriteRepoFile(t, baseRepo, ".c2/config.yaml", "self:\n  cell: app\n  repo: "+celltest.Repo(baseRepo)+"\n  ref: main\n")
	commitRepo(t, baseRepo, "add self config and default recipe")

	underlying := toyruntime.New()
	celltest.Create(t, underlying, tenantID, "app", celltest.Repo(baseRepo))
	server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(underlying)))
	defer server.Close()

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:   testJobDBURI(server.URL, tenantID),
		WorkingDir: baseRepo,
		JSONOutput: true,
		Prompt:     "build the current cell",
		Recipe:     "build",
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

func TestRun_ConventionsUseTargetCellNotSubmittingCell(t *testing.T) {
	t.Parallel()
	for _, selection := range []string{"build"} {
		t.Run("recipe="+selection, func(t *testing.T) {
			const kind = "build"
			ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
			defer cancel()
			targetRepo, _ := createGitRepo(t)
			submittingRepo, _ := createGitRepo(t)
			mustWriteRepoFile(t, targetRepo, ".c2/recipes/"+kind+".yaml", `
id: target_recipe
version: "1.0"
input_schema:
  prompt:
    type: string
    required: true
  type:
    type: string
    required: true
inputs:
  prompt: "{{ inputs.prompt }}"
  type: "{{ inputs.type }}"
sequence: []
outputs:
  prompt: "{{ inputs.prompt }}"
  type: "{{ inputs.type }}"
  source: target
`)
			commitRepo(t, targetRepo, "target recipe")
			mustWriteRepoFile(t, submittingRepo, ".c2/recipes/"+kind+".yaml", "invalid: [")
			mustWriteRepoFile(t, submittingRepo, ".c2/config.yaml", "self:\n  cell: app\n  repo: "+celltest.Repo(submittingRepo)+"\n  ref: main\n")
			commitRepo(t, submittingRepo, "unrelated submitting cell")
			underlying := toyruntime.New()
			celltest.Create(t, underlying, "target-conventions", "app", celltest.Repo(targetRepo))
			server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(underlying)))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			err := Run(ctx, Options{
				JobDBURI:   testJobDBURI(server.URL, "target-conventions"),
				WorkingDir: submittingRepo, Cell: "app", CellRepo: celltest.Repo(targetRepo), Recipe: selection,
				RunAfterSubmit: true, Stdin: strings.NewReader("interactive prompt\n"),
				Stdout: &stdout, Stderr: &stderr,
			})
			if err != nil {
				t.Fatalf("submit and run: %v\n%s", err, &stderr)
			}
			if !strings.Contains(stderr.String(), "Prompt: ") {
				t.Fatalf("missing interactive prompt: %s", &stderr)
			}
			firstLine, _, _ := strings.Cut(stdout.String(), "\n")
			if !strings.Contains(firstLine, "recipe="+kind) {
				t.Fatalf("unexpected submission: %s", firstLine)
			}
			runtime, err := remoteruntime.New(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
			if err != nil {
				t.Fatal(err)
			}
			run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{
				JobKey: jobdb.JobKey{TenantId: "target-conventions", JobId: extractSubmittedJobID(firstLine)}, IncludeOutputs: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			output, err := run.GetOutput(engine, "target-conventions")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := output.GetData()
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got["source"] != "target" || got["prompt"] != "interactive prompt" || got["type"] != kind {
				t.Fatalf("wrong target/inputs: %#v", got)
			}
		})
	}
}

func TestRun_SubmitsPromptAndRunsJobImmediately(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"positional", "relative file", "absolute file"} {
		t.Run(source, func(t *testing.T) {

			ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
			defer cancel()

			tenantID := "tenant-submit-prompt-test"
			recipeYAML := strings.TrimSpace(`
id: prompt_submit_recipe
desc: verifies prompt input and immediate execution
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
			celltest.Create(t, underlying, tenantID, "app", celltest.Repo(baseRepo))
			server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(underlying)))
			defer server.Close()

			var submitStdout bytes.Buffer
			var submitStderr bytes.Buffer
			opts := Options{
				JobDBURI:   testJobDBURI(server.URL, tenantID),
				RecipeFile: recipePath,
				Cell:       "app", CellRepo: celltest.Repo(baseRepo),
				Prompt:         "hello from positional prompt",
				PromptSet:      true,
				RunAfterSubmit: true,
				Stdin:          bytes.NewBuffer(nil),
				Stdout:         &submitStdout,
				Stderr:         &submitStderr,
			}
			prompt := "hello from positional prompt"
			if source != "positional" {
				prompt = "  Multiline prompt — preserve whitespace.\nSecond line.\n"
				opts.WorkingDir = t.TempDir()
				opts.PromptFile = "prompt.md"
				path := filepath.Join(opts.WorkingDir, opts.PromptFile)
				if err := os.WriteFile(path, []byte(prompt), 0o600); err != nil {
					t.Fatal(err)
				}
				if source == "absolute file" {
					opts.PromptFile = path
				}
				opts.Prompt, opts.PromptSet = "", false
				opts.PromptFileSet = true
			}
			if err := Run(ctx, opts); err != nil {
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
			if got["received_prompt"] != prompt {
				t.Fatalf("unexpected output: %#v", got)
			}
		})
	}
}

func TestRun_SubmitsAndExecutesWithEmbeddedRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 90*time.Second)
	defer cancel()

	t.Setenv("HOME", t.TempDir())

	recipeYAML := strings.TrimSpace(`
id: embed_submit_recipe
execution: {capabilities: [synthetic.recipe]}
input_schema:
  prompt:
    type: string
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

	handle, err := swfruntime.Open(ctx, defaults.EmbedURL)
	if err != nil {
		t.Fatal(err)
	}
	celltest.Create(t, handle.Runtime, defaults.EmbeddedTenantID, "app", celltest.Repo(baseRepo))
	if err := handle.Cleanup(); err != nil {
		t.Fatal(err)
	}

	var submitStdout bytes.Buffer
	if err := Run(ctx, Options{
		JobDBURI:              "embed:///",
		RecipeFile:            recipePath,
		ExecutionRequirements: execution.Requirements{Capabilities: []string{"synthetic.job"}},
		Prompt:                "run the embedded test recipe",
		Cell:                  "app", CellRepo: celltest.Repo(baseRepo),
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
		Allocation:   execution.Allocation{SchemaVersion: 1, Capabilities: []string{"synthetic.job", "synthetic.recipe"}},
		JobDBURI:     "embed:///",
		WaitTimeout:  15 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &runStdout,
		Stderr:       &runStderr,
	}); err != nil {
		t.Fatalf("run submitted job: %v\nstderr:\n%s", err, runStderr.String())
	}

	handle, err = swfruntime.Open(ctx, "embed:///")
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
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 90*time.Second)
	defer cancel()

	t.Setenv("HOME", t.TempDir())

	baseRepo, _ := createGitRepo(t)
	mustWriteRepoFile(t, baseRepo, ".c2/recipes/failing_extension_recipe.yaml", strings.TrimSpace(`
id: failing_extension_recipe
input_schema:
  prompt:
    type: string
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

	handle, err := swfruntime.Open(ctx, defaults.EmbedURL)
	if err != nil {
		t.Fatal(err)
	}
	celltest.Create(t, handle.Runtime, defaults.EmbeddedTenantID, "app", celltest.Repo(baseRepo))
	if err := handle.Cleanup(); err != nil {
		t.Fatal(err)
	}

	var submitStdout bytes.Buffer
	var submitStderr bytes.Buffer
	err = Run(ctx, Options{
		JobDBURI: "embed:///",
		Recipe:   "failing_extension_recipe",
		Prompt:   "exercise the failing extension",
		Cell:     "app", CellRepo: celltest.Repo(baseRepo),
		RunAfterSubmit: true,
		Stdin:          bytes.NewBuffer(nil),
		Stdout:         &submitStdout,
		Stderr:         &submitStderr,
	})
	if err == nil {
		t.Fatal("expected submit --run to fail")
	}
	errText := err.Error()
	const wantFailure = "extension failure [exit 23]"
	if errText != wantFailure {
		t.Fatalf("expected compact extension failure %q, got %v\nstderr:\n%s", wantFailure, err, submitStderr.String())
	}
	if strings.Contains(errText, "workflow state conflict") || strings.Contains(errText, "chapter ordinal") {
		t.Fatalf("chapter conflict masked extension failure: %v\nstderr:\n%s", err, submitStderr.String())
	}

	firstLine, _, _ := strings.Cut(submitStdout.String(), "\n")
	jobID := extractSubmittedJobID(firstLine)
	if jobID == "" {
		t.Fatalf("expected job id in submit output: %q", submitStdout.String())
	}

	handle, err = swfruntime.Open(ctx, "embed:///")
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
	} else if !strings.Contains(err.Error(), wantFailure) {
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

func TestRun_BrokerSubmissionAndConfiguredImmediateExecution(t *testing.T) {
	ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 30*time.Second)
	defer cancel()
	root, _ := createGitRepo(t)
	rt := toyruntime.New()
	celltest.Create(t, rt, "broker-run", "app", celltest.Repo(root))
	sdk, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt}
	ctx = jobref.WithScope(ctx, jobref.Scope{ResolveCell: cellref.Registry(rt, "broker-run")})
	parent, err := engine.SubmitJobRef(ctx, jobdb.SubmitJob{TenantId: "broker-run", JobID: "parent", JobType: "work", Cell: "app", CellRepo: celltest.Repo(root), Data: jobdb.NewTaskDataOrPanic(nil)})
	require.NoError(t, err)
	lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: parent.Key(), WorkerID: "parent", Routes: []jobdb.Route{{JobType: "work"}}, LeaseDuration: time.Minute})
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, jobdbschema.Register(ctx, rt, "broker-run"))
	current := jobcontext.Current{TenantID: "broker-run", GoalID: parent.GoalID, JobID: "parent", InvocationHash: "parent-invocation"}
	broker, err := childbroker.Start(ctx, childbroker.Options{Current: current, Submitter: waitLeaseSubmitter{lease}})
	require.NoError(t, err)
	defer broker.Close()
	for k, v := range jobcontext.EnvForCurrent(current) {
		t.Setenv(k, v)
	}
	for k, v := range broker.Env() {
		t.Setenv(k, v)
	}
	var directPublications atomic.Int32
	handler := celltest.Handler(remoteruntime.NewConsumerServer(rt))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/jobs") {
			directPublications.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "recipe.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`id: broker_run
version: "1"
input_schema:
 prompt: {type: string}
inputs:
 prompt: "{{ inputs.prompt }}"
sequence: []
outputs:
 received: "{{ inputs.prompt }}"
`), 0600))
	var out bytes.Buffer
	require.NoError(t, Run(ctx, Options{JobDBURI: server.URL + "/broker-run", WorkingDir: root, Cell: "app", RecipeFile: path, Prompt: "execute through direct runtime", RunAfterSubmit: true, Stdout: &out, Stderr: &out, Stdin: strings.NewReader("")}), out.String())
	started := broker.StartedJobs().Items
	require.Len(t, started, 1)
	require.Zero(t, directPublications.Load())
	run, err := sdk.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: "broker-run", JobId: started[0].JobID}, IncludeOutputs: true})
	require.NoError(t, err)
	output, err := run.GetOutput(sdk, "broker-run")
	require.NoError(t, err)
	raw, err := output.GetData()
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(raw, &result))
	require.Equal(t, "execute through direct runtime", result["received"])
	jobs, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"broker-run"}})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 2)
	parentagetest.Check(t, ctx, rt, parent.Key(), started[0].JobID)
}

func TestRecipeSubmissionInputsCLISources(t *testing.T) {
	for _, source := range []string{"positional", "prompt-file", "json", "json-file", "yaml-file"} {
		t.Run(source, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 20*time.Second)
			defer cancel()
			root := t.TempDir()
			path := filepath.Join(root, "submission-inputs.yaml")
			require.NoError(t, os.WriteFile(path, []byte("id: submission-inputs\ninputs: {all: '${{ inputs }}'}\nsequence: []\noutputs: {all: '${{ inputs.all }}'}\n"), 0600))
			repo, _ := createGitRepo(t)
			rt := toyruntime.New()
			celltest.Create(t, rt, "submission-inputs", "app", celltest.Repo(repo))
			server := httptest.NewServer(celltest.Handler(remoteruntime.NewConsumerServer(rt)))
			defer server.Close()
			prompt, title := "  Inspect café\n日本語  ", "  Title\nλ  "
			var stdout, stderr bytes.Buffer
			opts := Options{JobDBURI: testJobDBURI(server.URL, "submission-inputs"), RecipeFile: path, Cell: "app", CellRepo: celltest.Repo(repo), Title: title, JSONOutput: true, Stdout: &stdout, Stderr: &stderr}
			switch source {
			case "positional":
				opts.Prompt = prompt
			case "prompt-file":
				opts.PromptFile = filepath.Join(root, "prompt.md")
				require.NoError(t, os.WriteFile(opts.PromptFile, []byte(prompt), 0600))
			case "json", "json-file":
				raw, err := json.Marshal(map[string]any{"prompt": prompt})
				require.NoError(t, err)
				if source == "json" {
					opts.InputsJSON = string(raw)
				} else {
					opts.InputsFile = filepath.Join(root, "inputs.json")
					require.NoError(t, os.WriteFile(opts.InputsFile, raw, 0600))
				}
			case "yaml-file":
				opts.InputsFile = filepath.Join(root, "inputs.yaml")
				// JSON double-quoted strings are also valid YAML scalar strings.
				scalar, err := json.Marshal(prompt)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(opts.InputsFile, []byte("prompt: "+string(scalar)+"\n"), 0600))
			}
			require.NoError(t, Run(ctx, opts), stderr.String())
			var submitted struct {
				JobID string `json:"job_id"`
			}
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &submitted))
			require.NotEmpty(t, submitted.JobID)
			require.NoError(t, runjob.Run(ctx, runjob.Options{JobID: submitted.JobID, JobDBURI: opts.JobDBURI, WaitTimeout: 5 * time.Second, PollInterval: 10 * time.Millisecond, InputMode: "fail", Stdout: &bytes.Buffer{}, Stderr: &stderr}), stderr.String())
			key := jobdb.JobKey{TenantId: "submission-inputs", JobId: submitted.JobID}
			chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			first, ok := chapters[0].Body.(jobdb.JobStartChapter)
			require.True(t, ok)
			var start workflowctl.StartJob
			require.NoError(t, json.Unmarshal(first.Input.Data, &start))
			require.Equal(t, map[string]any{"title": title, "prompt": prompt}, start.Inputs)
			require.Equal(t, recipejob.InputHash(start.Inputs), start.InputHash)
			sdk, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			output, err := swfutil.JobResult(ctx, sdk, key)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(output.GetDataOrPanic(), &got))
			require.Equal(t, map[string]any{}, got["all"])
			page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"submission-inputs"}, JobKeys: []jobdb.JobKey{key}})
			require.NoError(t, err)
			require.Len(t, page.Jobs, 1)
			var meta starter.JobMetadata
			require.NoError(t, json.Unmarshal(page.Jobs[0].Metadata, &meta))
			require.Equal(t, title, meta.Title)
			require.Equal(t, prompt, meta.Prompt)
		})
	}
}
