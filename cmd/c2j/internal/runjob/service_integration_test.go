package runjob

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	remoteruntime "github.com/colony-2/swf-go/pkg/swf/runtime/remote"
	toyruntime "github.com/colony-2/swf-go/pkg/swf/runtime/toy"
)

func TestRun_CompletesJobAndReplaysCachedHistory(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-c2j-test"
	recipeDir := t.TempDir()
	recipeYAML := strings.TrimSpace(`
id: nucleus_test_recipe
desc: simple recipe used to verify c2j remote execution
version: "1.0"
sequence:
  - id: echo
    op: command_execution
    inputs:
      run: "echo hello-from-c2j"
      working_directory: "."
outputs:
  result: "{{ sequence.echo.outputs.stdout }}"
`) + "\n"

	recipePath := filepath.Join(recipeDir, "nucleus_test_recipe.yaml")
	if err := os.WriteFile(recipePath, []byte(recipeYAML), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	rec, err := recipe.LoadRecipeFromString([]byte(recipeYAML))
	if err != nil {
		t.Fatalf("load recipe: %v", err)
	}

	underlying := toyruntime.New()
	submitEngine, err := swf.NewEngineBuilder().WithRuntime(underlying).BuildEngine()
	if err != nil {
		t.Fatalf("build submit engine: %v", err)
	}

	baseRepo, baseHash := createGitRepo(t)

	jobKey, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{
		TenantId:   tenantID,
		RecipeName: rec.GetMetadata().ID,
		Inputs:     map[string]any{},
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{
				ProjectId: tenantID,
				CellName:  ".",
				CellPath:  ".",
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo:         baseRepo,
				BaseRef:          baseHash,
				ResolvedBaseHash: baseHash,
			},
		},
		GitRef: baseHash,
	}, submitEngine, *rec)
	if err != nil {
		t.Fatalf("start recipe job: %v", err)
	}

	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	var liveStdout bytes.Buffer
	var liveStderr bytes.Buffer
	if err := Run(ctx, Options{
		JobID:        jobKey.JobId,
		TenantID:     tenantID,
		SWFURL:       server.URL,
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &liveStdout,
		Stderr:       &liveStderr,
	}); err != nil {
		t.Fatalf("first run: %v\nstderr:\n%s", err, liveStderr.String())
	}

	if err := swf.WaitForJobToComplete(ctx, 5*time.Second, jobKey, submitEngine); err != nil {
		t.Fatalf("wait for completion: %v", err)
	}

	run, err := submitEngine.GetJobRun(ctx, swf.GetJobRunRequest{
		JobKey:         jobKey,
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	output, err := run.GetOutput(submitEngine, tenantID)
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
	if got["result"] != "hello-from-c2j" {
		t.Fatalf("unexpected output: %#v", got)
	}

	if out := liveStdout.String(); !strings.Contains(out, "[live]") || !strings.Contains(out, "command_execution") {
		t.Fatalf("expected live execution output, got:\n%s", out)
	}

	var cachedStdout bytes.Buffer
	var cachedStderr bytes.Buffer
	if err := Run(ctx, Options{
		JobID:        jobKey.JobId,
		TenantID:     tenantID,
		SWFURL:       server.URL,
		WaitTimeout:  5 * time.Second,
		PollInterval: 10 * time.Millisecond,
		InputMode:    "fail",
		Stdout:       &cachedStdout,
		Stderr:       &cachedStderr,
	}); err != nil {
		t.Fatalf("second run: %v\nstderr:\n%s", err, cachedStderr.String())
	}

	if out := cachedStdout.String(); !strings.Contains(out, "[cached]") || strings.Contains(out, "[live]") {
		t.Fatalf("expected cached replay only, got:\n%s", out)
	}
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
