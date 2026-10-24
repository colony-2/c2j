package workjob

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/c2jops"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	remoteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

func testJobDBURI(serverURL string, tenantID string) string {
	return strings.TrimRight(serverURL, "/") + "/" + tenantID
}

func newSchemaAwareSubmitEngine(t *testing.T, runtime jobdb.WorkflowRuntime) jobworkflow.Engine {
	t.Helper()
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	if err != nil {
		t.Fatalf("build submit engine: %v", err)
	}
	registry, ok := runtime.(jobdb.JobSchemaRegistry)
	if !ok {
		t.Fatalf("runtime does not implement job schema registry")
	}
	return jobdbschema.WorkflowEngine{Engine: engine, Registry: registry}
}

func TestReadyCountsAvailableJobsByTenant(t *testing.T) {
	c2jops.Register()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-ready-count"
	otherTenantID := "tenant-ready-other"
	underlying := toyruntime.New()
	submitEngine := newSchemaAwareSubmitEngine(t, underlying)
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	recipeYAML := simpleSuccessRecipe("ready_count_recipe", "ready-count")
	for i := 0; i < 3; i++ {
		submitRecipeJob(t, ctx, submitEngine, tenantID, recipeYAML)
	}
	submitRecipeJob(t, ctx, submitEngine, otherTenantID, recipeYAML)

	var stdout, stderr bytes.Buffer
	if err := Ready(ctx, ReadyOptions{
		JobDBURI:   testJobDBURI(server.URL, tenantID),
		WorkingDir: t.TempDir(),
		Stdout:     &stdout,
		Stderr:     &stderr,
	}); err != nil {
		t.Fatalf("Ready(): %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "3" {
		t.Fatalf("Ready() stdout = %q, want 3", stdout.String())
	}

	count, err := CountReady(ctx, ReadyOptions{
		JobDBURI:   testJobDBURI(server.URL, otherTenantID),
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("CountReady(other tenant): %v", err)
	}
	if count != 1 {
		t.Fatalf("other tenant ready count = %d, want 1", count)
	}
}

func submitRecipeJob(t *testing.T, ctx context.Context, engine jobworkflow.Engine, tenantID string, recipeYAML string) jobdb.JobKey {
	t.Helper()

	rec, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(recipeYAML) + "\n"))
	if err != nil {
		t.Fatalf("load recipe: %v\n%s", err, recipeYAML)
	}
	baseRepo, baseHash := workCreateGitRepo(t)
	keyRef, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{
		TenantId:   tenantID,
		RecipeName: rec.GetMetadata().ID,
		Inputs:     map[string]any{},
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{
				ProjectId: tenantID,
				CellName:  ".",
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo:         baseRepo,
				BaseRef:          baseHash,
				ResolvedBaseHash: baseHash,
			},
		},
		GitRef: baseHash,
	}, engine, *rec)
	key := keyRef.Key()
	if err != nil {
		t.Fatalf("start recipe job: %v", err)
	}
	return key
}

func simpleSuccessRecipe(id string, stdout string) string {
	return fmt.Sprintf(`
id: %s
desc: succeeds for worker command tests
version: "1.0"
sequence:
  - id: ok
    op: command_execution
    inputs:
      run: "echo %s"
      working_directory: "."
outputs:
  result: "{{ sequence.ok.outputs.stdout }}"
`, id, stdout)
}

func workCreateGitRepo(t *testing.T) (string, string) {
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
	run("git", "config", "user.name", "C2J Work Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("c2j ready\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	return dir, run("git", "rev-parse", "HEAD")
}
