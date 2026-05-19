package workjob

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
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

func TestRunProcessesSubmittedJobsWithConcurrencyLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-work-concurrency"
	underlying := toyruntime.New()
	submitEngine, err := swf.NewEngineBuilder().WithRuntime(underlying).BuildEngine()
	if err != nil {
		t.Fatalf("build submit engine: %v", err)
	}
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	logPath := filepath.Join(t.TempDir(), "timeline.log")
	recipeYAML := fmt.Sprintf(`
id: work_concurrency_recipe
desc: records concurrent command execution
version: "1.0"
sequence:
  - id: timed
    op: command_execution
    inputs:
      run: |
        printf 'start %%s\n' "$(date +%%s%%N)" >> %s
        sleep 0.35
        printf 'end %%s\n' "$(date +%%s%%N)" >> %s
      working_directory: "."
      shell: bash
outputs:
  done: "{{ sequence.timed.outputs.success }}"
`, shellQuote(logPath), shellQuote(logPath))

	keys := make([]swf.JobKey, 0, 4)
	for i := 0; i < 4; i++ {
		keys = append(keys, submitRecipeJob(t, ctx, submitEngine, tenantID, recipeYAML))
	}

	var stdout, stderr bytes.Buffer
	workerCtx, stopWorker := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(workerCtx, Options{
			TenantID:       tenantID,
			SWFURL:         server.URL,
			Concurrency:    2,
			AwaitThreshold: 30 * time.Second,
			WorkingDir:     t.TempDir(),
			Stdout:         &stdout,
			Stderr:         &stderr,
		})
	}()
	t.Cleanup(stopWorker)

	for _, key := range keys {
		if err := swf.WaitForJobToComplete(ctx, 10*time.Second, key, submitEngine); err != nil {
			t.Fatalf("wait for %s: %v\nworker stdout:\n%s\nworker stderr:\n%s", key, err, stdout.String(), stderr.String())
		}
		got := jobOutputMap(t, ctx, submitEngine, tenantID, key)
		if got["done"] != true {
			t.Fatalf("job %s output = %#v, want done=true", key, got)
		}
	}

	stopWorker()
	if err := waitForWorkerExit(ctx, errCh); err != nil {
		t.Fatalf("worker returned error: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if out := stdout.String(); !strings.Contains(out, "working tenant="+tenantID) || !strings.Contains(out, "concurrency=2") {
		t.Fatalf("unexpected startup output:\n%s", out)
	}

	maxActive := maxActiveFromTimeline(t, logPath)
	if maxActive != 2 {
		t.Fatalf("max active jobs = %d, want 2\ntimeline:\n%s", maxActive, readFileForFailure(t, logPath))
	}
}

func TestRunOnlyPollsConfiguredTenant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-work-selected"
	otherTenantID := "tenant-work-other"
	underlying := toyruntime.New()
	submitEngine, err := swf.NewEngineBuilder().WithRuntime(underlying).BuildEngine()
	if err != nil {
		t.Fatalf("build submit engine: %v", err)
	}
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	recipeYAML := `
id: work_tenant_recipe
desc: succeeds when picked by the configured tenant worker
version: "1.0"
sequence:
  - id: ok
    op: command_execution
    inputs:
      run: "echo tenant-job"
      working_directory: "."
outputs:
  result: "{{ sequence.ok.outputs.stdout }}"
`

	selectedKey := submitRecipeJob(t, ctx, submitEngine, tenantID, recipeYAML)
	otherKey := submitRecipeJob(t, ctx, submitEngine, otherTenantID, recipeYAML)

	var stdout, stderr bytes.Buffer
	workerCtx, stopWorker := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(workerCtx, Options{
			TenantID:       tenantID,
			SWFURL:         server.URL,
			Concurrency:    1,
			AwaitThreshold: 30 * time.Second,
			WorkingDir:     t.TempDir(),
			Stdout:         &stdout,
			Stderr:         &stderr,
		})
	}()
	t.Cleanup(stopWorker)

	if err := swf.WaitForJobToComplete(ctx, 10*time.Second, selectedKey, submitEngine); err != nil {
		t.Fatalf("wait for selected tenant job: %v\nworker stdout:\n%s\nworker stderr:\n%s", err, stdout.String(), stderr.String())
	}
	got := jobOutputMap(t, ctx, submitEngine, tenantID, selectedKey)
	if got["result"] != "tenant-job" {
		t.Fatalf("selected tenant output = %#v, want tenant-job", got)
	}

	assertJobRemainsStatus(t, ctx, submitEngine, otherKey, swf.JobStatusReady, 750*time.Millisecond)

	stopWorker()
	if err := waitForWorkerExit(ctx, errCh); err != nil {
		t.Fatalf("worker returned error: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
}

func TestRunContinuesAfterCommandErrorWhenRecipeContinues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := "tenant-work-failure"
	underlying := toyruntime.New()
	submitEngine, err := swf.NewEngineBuilder().WithRuntime(underlying).BuildEngine()
	if err != nil {
		t.Fatalf("build submit engine: %v", err)
	}
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	failingRecipe := `
id: work_failure_recipe
desc: records a command error without failing the job
version: "1.0"
sequence:
  - id: fail
    op: command_execution
    inputs:
      run: "echo failing; exit 7"
      working_directory: "."
      continue_on_error: true
outputs:
  success: "{{ sequence.fail.outputs.success }}"
  exit_code: "{{ sequence.fail.outputs.exit_code }}"
`
	successRecipe := `
id: work_success_recipe
desc: succeeds after a failing job exists
version: "1.0"
sequence:
  - id: ok
    op: command_execution
    inputs:
      run: "echo worker-still-running"
      working_directory: "."
outputs:
  result: "{{ sequence.ok.outputs.stdout }}"
`

	commandErrorKey := submitRecipeJob(t, ctx, submitEngine, tenantID, failingRecipe)
	successKey := submitRecipeJob(t, ctx, submitEngine, tenantID, successRecipe)

	var stdout, stderr bytes.Buffer
	workerCtx, stopWorker := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(workerCtx, Options{
			TenantID:       tenantID,
			SWFURL:         server.URL,
			Concurrency:    1,
			AwaitThreshold: 30 * time.Second,
			WorkingDir:     t.TempDir(),
			Stdout:         &stdout,
			Stderr:         &stderr,
		})
	}()
	t.Cleanup(stopWorker)

	for _, key := range []swf.JobKey{commandErrorKey, successKey} {
		if err := swf.WaitForJobToComplete(ctx, 10*time.Second, key, submitEngine); err != nil {
			t.Fatalf("wait for %s: %v\nworker stdout:\n%s\nworker stderr:\n%s", key, err, stdout.String(), stderr.String())
		}
	}

	stopWorker()
	if err := waitForWorkerExit(ctx, errCh); err != nil {
		t.Fatalf("worker returned error: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	commandErrorOutput := jobOutputMap(t, ctx, submitEngine, tenantID, commandErrorKey)
	if commandErrorOutput["success"] != false {
		t.Fatalf("command error success = %#v, want false", commandErrorOutput["success"])
	}
	if commandErrorOutput["exit_code"] != float64(7) {
		t.Fatalf("command error exit_code = %#v, want 7", commandErrorOutput["exit_code"])
	}

	got := jobOutputMap(t, ctx, submitEngine, tenantID, successKey)
	if got["result"] != "worker-still-running" {
		t.Fatalf("success output = %#v, want worker-still-running", got)
	}
}

func TestRunReturnsAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantID := "tenant-work-cancel"
	underlying := toyruntime.New()
	server := httptest.NewServer(remoteruntime.NewServer(underlying))
	defer server.Close()

	workerCtx, stopWorker := context.WithCancel(ctx)
	stopWorker()
	var stdout, stderr bytes.Buffer
	if err := Run(workerCtx, Options{
		TenantID:    tenantID,
		SWFURL:      server.URL,
		Concurrency: 1,
		WorkingDir:  t.TempDir(),
		Stdout:      &stdout,
		Stderr:      &stderr,
	}); err != nil {
		t.Fatalf("Run() after cancellation: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
}

func submitRecipeJob(t *testing.T, ctx context.Context, engine swf.SWFEngine, tenantID string, recipeYAML string) swf.JobKey {
	t.Helper()

	rec, err := recipe.LoadRecipeFromString([]byte(strings.TrimSpace(recipeYAML) + "\n"))
	if err != nil {
		t.Fatalf("load recipe: %v\n%s", err, recipeYAML)
	}
	baseRepo, baseHash := workCreateGitRepo(t)
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{
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
	if err != nil {
		t.Fatalf("start recipe job: %v", err)
	}
	return key
}

func jobOutputMap(t *testing.T, ctx context.Context, engine swf.SWFEngine, tenantID string, key swf.JobKey) map[string]any {
	t.Helper()

	run, err := engine.GetJobRun(ctx, swf.GetJobRunRequest{
		JobKey:         key,
		IncludeOutputs: true,
	})
	if err != nil {
		t.Fatalf("get job run: %v", err)
	}
	output, err := run.GetOutput(engine, tenantID)
	if err != nil {
		t.Fatalf("get job output: %v", err)
	}
	raw, err := output.GetData()
	if err != nil {
		t.Fatalf("get job output data: %v", err)
	}
	got := map[string]any{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode job output: %v", err)
	}
	return got
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func waitForWorkerExit(ctx context.Context, errCh <-chan error) error {
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return fmt.Errorf("worker did not exit before context ended: %w", ctx.Err())
	}
}

func assertJobRemainsStatus(t *testing.T, ctx context.Context, engine swf.SWFEngine, key swf.JobKey, want swf.JobStatus, duration time.Duration) {
	t.Helper()

	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		info, err := engine.GetJob(ctx, key)
		if err != nil {
			t.Fatalf("get job %s: %v", key, err)
		}
		if info.Status != want {
			t.Fatalf("job %s status = %s, want it to remain %s", key, info.Status, want)
		}

		select {
		case <-deadline.C:
			return
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("context ended while waiting for job %s to remain %s: %v", key, want, ctx.Err())
		}
	}
}

type timelineEvent struct {
	at    int64
	delta int
	line  string
}

func maxActiveFromTimeline(t *testing.T, path string) int {
	t.Helper()

	raw := readFileForFailure(t, path)
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	events := make([]timelineEvent, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kind, value, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("timeline line %q is not in KIND TIMESTAMP form", line)
		}
		at, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			t.Fatalf("timeline timestamp in %q: %v", line, err)
		}
		switch kind {
		case "start":
			events = append(events, timelineEvent{at: at, delta: 1, line: line})
		case "end":
			events = append(events, timelineEvent{at: at, delta: -1, line: line})
		default:
			t.Fatalf("timeline line %q has unsupported kind", line)
		}
	}
	if len(events) == 0 {
		t.Fatalf("timeline %s is empty", path)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].at == events[j].at {
			return events[i].delta < events[j].delta
		}
		return events[i].at < events[j].at
	})

	active := 0
	maxActive := 0
	for _, event := range events {
		active += event.delta
		if active < 0 {
			t.Fatalf("timeline became negative after %q\n%s", event.line, raw)
		}
		if active > maxActive {
			maxActive = active
		}
	}
	if active != 0 {
		t.Fatalf("timeline ended with active=%d\n%s", active, raw)
	}
	return maxActive
}

func readFileForFailure(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
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
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("c2j work\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	return dir, run("git", "rev-parse", "HEAD")
}
