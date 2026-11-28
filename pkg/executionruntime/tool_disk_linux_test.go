package executionruntime

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/projectcatalog"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestToolDiskWorkerRecovery(t *testing.T) {
	for _, mode := range []string{"handoff", "crash concern", "no allowance", "application ENOSPC", "installer output", "nix task 146", "nix task 149", "nix form task 161", "nix eval full", "nix wrong store", "nix directory overrides"} {
		t.Run(mode, func(t *testing.T) {
			nix := strings.HasPrefix(mode, "nix ")
			form := mode == "nix form task 161"
			wrongStore := mode == "nix wrong store"
			customLayout := mode == "nix directory overrides"
			handoff := mode == "handoff" || mode == "crash concern" || nix && mode != "nix eval full" && !wrongStore && !customLayout
			ctx := celltest.Context(context.Background())
			previous := coreops.List()
			t.Cleanup(func() { coreops.Replace(previous...) })
			root, bin := t.TempDir(), t.TempDir()
			t.Setenv("C2J_TOOL_CACHE_DIR", t.TempDir())
			t.Setenv("PULSE_LAUNCH_ID", "3700a99b2f3bde156ff465bd73676248")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("C2J_TEST_SETUP_MODE", "healthy")
			if mode == "handoff" || mode == "crash concern" || mode == "no allowance" {
				t.Setenv("C2J_TEST_SETUP_MODE", "full")
			} else if mode == "installer output" {
				t.Setenv("C2J_TEST_SETUP_MODE", "text")
			}
			require.NoError(t, os.WriteFile(filepath.Join(bin, "uv"), []byte(`#!/bin/sh
set -eu
case "$C2J_TEST_SETUP_MODE" in
  full) ln -s /dev/full ready.json.tmp ;;
  text) echo "error: creating directory '/nix/store/cmark': No space left on device" >&2; exit 1 ;;
esac
mkdir -p "$UV_TOOL_BIN_DIR"
printf '#!/bin/sh\n' > "$UV_TOOL_BIN_DIR/disk-fixture"
chmod +x "$UV_TOOL_BIN_DIR/disk-fixture"
`), 0700))
			for _, args := range [][]string{{"init", "-b", "main"}, {"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "disk fixture"}} {
				output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
				require.NoError(t, err, "%s", output)
			}
			var prior, body atomic.Int32
			coreops.Register(coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "disk_prior"}, func(deps coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				prior.Add(1)
				return nil, os.WriteFile(filepath.Join(deps.WorktreePath(), "prior.txt"), []byte("durable"), 0600)
			}))
			coreops.Register(coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "disk_affected"}, func(deps coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				body.Add(1)
				if mode == "application ENOSPC" {
					return nil, os.WriteFile("/dev/full", []byte("already started"), 0600)
				}
				data, err := os.ReadFile(filepath.Join(deps.WorktreePath(), "prior.txt"))
				return map[string]any{"result": string(data)}, err
			}))
			pkg, node := "uv:disk-fixture", "op: disk_affected"
			outputField := "result"
			var failedPath, diagnostic string
			const githubNix = "nix:github:NixOS/nixpkgs/e4074f13905f5117d20ad71358596b41694863aa#"
			if nix {
				pkg = githubNix + "cmark"
				failedPath, diagnostic = installDiskNixFixture(t, bin, mode)
			}
			var generated atomic.Int32
			if form {
				coreops.Register(input.GetAutoFillOp(), countedDiskForm{RegisterableOp: input.GetOp(), started: &generated})
				pkg = githubNix + "python3"
				node = "op: input\n   inputs: {form: {question: Review, type: short_answer, autofill: {response: durable}}}"
				outputField = "response"
			}
			rec, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf(`id: tool-disk-fixture
sequence:
 - id: prior
   op: disk_prior
 - id: affected
   execution_needs: {packages: [%s]}
   %s
   catch:
    - continue: {outputs: {%s: caught, error: '${{ failure.message }}'}}
outputs:
 result: "${{ sequence.affected.outputs.%s }}"
 error: "${{ has(sequence.affected.outputs.error) ? sequence.affected.outputs.error : '' }}"
`, pkg, node, outputField, outputField)))
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), "disk.db")
			backend, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: path})
			require.NoError(t, err)
			defer backend.Close(ctx)
			admin := jobdb.WithProjectAdministration(ctx, func(context.Context) error { return nil })
			db, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			inventory, err := sqlite.InspectProjectInstallation(admin, db, projectcatalog.InstallationDigest([]byte("isolated disk recovery test")))
			require.NoError(t, err)
			require.NoError(t, sqlite.InstallProjectCatalog(admin, db, inventory, []projectcatalog.Seed{{ID: "c2", Name: "c2"}}))
			require.NoError(t, db.Close())
			celltest.Create(t, backend, "c2", "disk", celltest.Repo(root))
			engine, err := wf.NewEngineBuilder().WithRuntime(backend).BuildEngine()
			require.NoError(t, err)
			job := contextual.JobContext{Workflow: contextual.WorkflowContext{CellName: "disk"}, GitBase: contextual.GitBaseContext{BaseRepo: root, BaseRef: "main"}}
			ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "c2", RecipeName: "tool-disk-fixture", JobContext: job, GitRef: "main", Cell: "disk", CellRepo: celltest.Repo(root)}, jobdbschema.WorkflowEngine{Engine: engine, Registry: backend}, *rec)
			require.NoError(t, err)
			var logs bytes.Buffer
			spent := 0
			if mode == "handoff" {
				spent = 3
			} else if mode == "no allowance" {
				spent = 5
			}
			for range spent {
				lease, err := backend.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: ref.Key(), WorkerID: "expired-fixture", LeaseDuration: 50 * time.Millisecond, Routes: []jobdb.Route{{JobType: "recipe"}}})
				require.NoError(t, err)
				require.NotNil(t, lease)
				require.Eventually(t, func() bool {
					job, err := backend.GetJob(ctx, ref.Key())
					return err == nil && job.Status == jobdb.JobStatusReady
				}, time.Second, 10*time.Millisecond)
			}
			run := func(worker string, duration time.Duration) (wf.JobRunOutcome, error) {
				guard := New(backend, execution.Allocation{SchemaVersion: execution.SchemaVersion})
				registry, err := workerops.NewActivityRegistry()
				require.NoError(t, err)
				wrap := func(worker wf.TaskWorker) wf.TaskWorker {
					return guard.WrapTaskWorker(diskDiagnosticWorker{TaskWorker: worker})
				}
				workers, err := compiler.NewRecipeWorkerWithOptions(coreops.NewServiceDepsBuilder().Build(), registry, compiler.RecipeJobWorkerOptions{WrapTaskWorker: wrap, StageExecution: guard.Stage})
				require.NoError(t, err)
				var tasks []wf.TaskWorker
				for _, task := range workers.TaskWorkers {
					tasks = append(tasks, task)
				}
				handle, err := wf.GetJobForRun(ctx, guard, wf.GetJobForRunRequest{JobKey: ref.Key(), JobWorker: workers.JobWorker, TaskWorkers: tasks, WorkerID: worker, LeaseDuration: duration, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
				require.NoError(t, err)
				return handle.Run(nil)
			}
			getJob := func() jobdb.JobInfo {
				info, err := backend.GetJob(ctx, ref.Key())
				require.NoError(t, err)
				return info
			}
			duration := time.Minute
			if mode == "no allowance" {
				duration = 750 * time.Millisecond
			}
			outcome, err := run("original-worker", duration)
			if mode == "no allowance" {
				require.ErrorContains(t, err, "cannot preserve remaining crash allowance")
				require.True(t, jobdb.IsExecutionInfrastructureError(err))
				require.Nil(t, getJob().ExecutionState.ResourceResume)
				require.Zero(t, body.Load())
				require.Eventually(t, func() bool { return getJob().Status == jobdb.JobStatusCrashConcern }, 4*time.Second, 20*time.Millisecond)
				return
			}
			require.NoError(t, err)
			if handoff {
				require.Equal(t, wf.JobRunSuspended, outcome.Status, "%v", outcome.JobError)
				require.Zero(t, body.Load())
				info := getJob()
				require.Equal(t, jobdb.JobStatusAwaitingFuture, info.Status)
				require.Zero(t, generated.Load())
				require.EqualValues(t, 1, prior.Load())
				require.NotNil(t, info.ExecutionState.ResourceResume)
				require.NotNil(t, info.ExecutionState.CrashBudgetRemaining)
				require.EqualValues(t, 5-spent, *info.ExecutionState.CrashBudgetRemaining)
				for _, text := range []string{"task.tool_setup.disk_exhausted", "3700a99b2f3bde156ff465bd73676248", "chapter_ordinal"} {
					require.Contains(t, logs.String(), text)
				}
				require.Eventually(t, func() bool { return getJob().Status == jobdb.JobStatusReady }, 4*time.Second, 20*time.Millisecond)
				if nix {
					assertDiskWarning(t, logs.String(), failedPath, diagnostic, form)
				} else {
					require.Contains(t, logs.String(), "ready.json.tmp")
					require.Contains(t, logs.String(), syscall.ENOSPC.Error())
				}
				if mode == "crash concern" {
					for round := 0; round < 8 && getJob().Status != jobdb.JobStatusCrashConcern; round++ {
						outcome, err := run("still-unhealthy", 750*time.Millisecond)
						require.ErrorContains(t, err, "disk exhausted again", "round=%d, outcome=%+v, resume=%+v, logs=%s", round, outcome, info.ExecutionState.ResourceResume, logs.String())
						require.True(t, jobdb.IsExecutionInfrastructureError(err))
						require.Eventually(t, func() bool { return getJob().Status != jobdb.JobStatusActive }, 4*time.Second, 20*time.Millisecond)
						require.Equal(t, info.ExecutionState.ResourceResume, getJob().ExecutionState.ResourceResume)
					}
					require.Equal(t, jobdb.JobStatusCrashConcern, getJob().Status)
					require.Zero(t, body.Load())
					return
				}
				t.Setenv("C2J_TEST_SETUP_MODE", "healthy")
				t.Setenv("C2J_TEST_NIX_FAILURE", "")
				outcome, err = run("compatible-worker", time.Minute)
				require.NoError(t, err)
			}
			require.Equal(t, wf.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			require.EqualValues(t, 1, prior.Load())
			var output map[string]any
			require.NoError(t, json.Unmarshal(outcome.Output.GetDataOrPanic(), &output))
			if handoff {
				require.Equal(t, "durable", output["result"], "%s", output["error"])
				if form {
					require.EqualValues(t, 1, generated.Load())
				} else {
					require.EqualValues(t, 1, body.Load())
				}
			} else if customLayout {
				require.Equal(t, "durable", output["result"], "%s", output["error"])
				require.EqualValues(t, 1, body.Load())
				require.NoFileExists(t, filepath.Join(bin, "nix-prefetch-ran"))
				require.Nil(t, getJob().ExecutionState.ResourceResume)
				require.NotContains(t, logs.String(), "task.tool_setup.disk_exhausted")
			} else {
				require.Equal(t, "caught", output["result"])
				if nix {
					require.Contains(t, output["error"], diagnostic)
				}
				if wrongStore {
					require.NoFileExists(t, filepath.Join(bin, "nix-prefetch-ran"))
					require.Nil(t, getJob().ExecutionState.ResourceResume)
				}
				require.Equal(t, mode == "application ENOSPC", body.Load() == 1)
				require.NotContains(t, logs.String(), "task.tool_setup.disk_exhausted")
			}
		})
	}
}

func TestToolDiskConfiguredStoreOffline(t *testing.T) {
	const ref = "github:c2j-test/offline-fixture/0000000000000000000000000000000000000001"
	nix, err := exec.LookPath("nix")
	if err != nil && os.Getenv("C2J_REQUIRE_TOOL_DISK_NIX") != "1" {
		t.Skip("offline store test requires Nix")
	}
	require.NoError(t, err)
	output := diskNixFixtureOutput(t)
	privateRoot := t.TempDir()
	t.Cleanup(func() {
		err := filepath.WalkDir(filepath.Join(privateRoot, "nix/store"), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0700)
			}
			return nil
		})
		if !os.IsNotExist(err) {
			require.NoError(t, err)
		}
	})
	privateStore := "local?root=" + privateRoot
	t.Setenv("NIX_REMOTE", "local")
	t.Setenv("NIX_CONFIG", "store = "+privateStore+"\nsubstituters =\nflake-registry =\n")
	t.Setenv("NIX_CACHE_HOME", t.TempDir())
	command := func(args ...string) string {
		t.Helper()
		args = append([]string{"--extra-experimental-features", "nix-command flakes read-only-local-store", "--offline"}, args...)
		var stderr bytes.Buffer
		cmd := exec.CommandContext(t.Context(), nix, args...)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		require.NoError(t, err, "nix %v: %s", args, stderr.String())
		return strings.TrimSpace(string(out))
	}
	command("copy", "--from", "local?read-only=true", "--to", privateStore, "--no-check-sigs", "--", output)
	require.DirExists(t, filepath.Join(privateRoot, output))
	system, err := toolenv.NixSystem()
	require.NoError(t, err)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "flake.nix"), []byte(fmt.Sprintf(`{
  outputs = _: { packages.%s.default = {
    drvPath = "%s.drv";
    outPath = "%s";
    outputName = "out";
  }; };
}
`, system, output, output)), 0600))
	sourceJSON := command("flake", "prefetch", "--json", "--", "path:"+source)
	var prefetched struct{ StorePath string }
	require.NoError(t, json.Unmarshal([]byte(sourceJSON), &prefetched))
	privateSource := filepath.Join(privateRoot, prefetched.StorePath)
	require.FileExists(t, filepath.Join(privateSource, "flake.nix"))
	bin := t.TempDir()
	trace := filepath.Join(bin, "trace")
	t.Setenv("C2J_TEST_REAL_NIX", nix)
	t.Setenv("C2J_TEST_NIX_REF", ref)
	t.Setenv("C2J_TEST_NIX_SOURCE", "path:"+privateSource)
	t.Setenv("C2J_TEST_NIX_TRACE", trace)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "nix"), []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$C2J_TEST_NIX_TRACE"
case " $* " in
  *' --store local '*) echo "error: opening lock file '/nix/var/nix/db/big-lock': Permission denied" >&2; exit 1 ;;
esac
for arg do
  shift
  case "$arg" in
    "$C2J_TEST_NIX_REF#default") arg="$C2J_TEST_NIX_SOURCE#default" ;;
  esac
  set -- "$@" "$arg"
done
exec "$C2J_TEST_REAL_NIX" --offline "$@"
`), 0700))
	manager := toolenv.Manager{Root: t.TempDir()}
	env, diagnostic, err := manager.Prepare(t.Context(), []toolenv.Scope{{ID: "op", Packages: []string{"nix:" + ref + "#default"}}})
	require.NoError(t, err)
	require.True(t, env.Ready())
	require.Equal(t, "prepared", diagnostic.Tools[0].Outcome)
	commands, err := os.ReadFile(trace)
	require.NoError(t, err)
	require.Contains(t, string(commands), " config show store")
	require.NotContains(t, string(commands), " flake prefetch ")
}

const diskFixtureSecret = "c2j-test-private-disk-value"

type diskDiagnosticWorker struct{ wf.TaskWorker }

func (worker diskDiagnosticWorker) RunTask(local context.Context, ctx wf.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	if err := secrets.RedactorFromContext(local).Register(diskFixtureSecret); err != nil {
		return nil, err
	}
	return task.RunLocal(worker.TaskWorker, local, ctx, input)
}

type countedDiskForm struct {
	coreops.RegisterableOp
	started *atomic.Int32
}

func (form countedDiskForm) TaskChain() []coreops.TaskStep {
	chain := append([]coreops.TaskStep(nil), form.RegisterableOp.TaskChain()...)
	for index := range chain {
		if chain[index].Name == "generate_form" {
			invoke := chain[index].Invoke
			chain[index].Invoke = func(deps coreops.OpDependencies, ctx context.Context, in map[string]any) (map[string]any, error) {
				form.started.Add(1)
				return invoke(deps, ctx, in)
			}
		}
	}
	return chain
}

func installDiskNixFixture(t *testing.T, bin, mode string) (string, string) {
	t.Helper()
	path := "/nix/store/tmp-175586-0/x/pkgs/misc/signed-packages/by-name/fw"
	if mode == "nix task 149" || mode == "nix directory overrides" {
		path = ""
	} else if mode == "nix form task 161" {
		path = "/nix/store/tmp-form-fixture/" + diskFixtureSecret
	}
	diagnostic := "writing to file: No space left on device"
	if path != "" {
		diagnostic = fmt.Sprintf("creating directory '%s': No space left on device", path)
	}
	t.Setenv("NIX_REMOTE", "local")
	t.Setenv("C2J_TEST_NIX_FAILURE", mode)
	t.Setenv("C2J_TEST_NIX_LOG", fmt.Sprintf(`@nix {"action":"msg","level":0,"raw_msg":%q}`, diagnostic))
	storeRoot := t.TempDir()
	setupWrite := filepath.Join(storeRoot, "nix/store", "store-write")
	require.NoError(t, os.MkdirAll(filepath.Dir(setupWrite), 0700))
	require.NoError(t, os.Symlink("/dev/full", setupWrite))
	t.Setenv("C2J_TEST_NIX_STORE_ROOT", storeRoot)
	t.Setenv("C2J_TEST_NIX_STORE_WRITE", setupWrite)
	if mode == "nix wrong store" {
		t.Setenv("NIX_CONFIG", "store = local?root="+storeRoot)
	}
	if mode == "nix directory overrides" {
		t.Setenv("NIX_CONFIG", "store = local")
		t.Setenv("NIX_STORE_DIR", filepath.Dir(setupWrite))
		t.Setenv("NIX_STATE_DIR", filepath.Join(storeRoot, "nix/var/nix"))
		require.ErrorIs(t, os.WriteFile(setupWrite, []byte("prefetch write target"), 0600), syscall.ENOSPC)
	}
	if mode == "nix eval full" {
		require.ErrorIs(t, os.WriteFile(setupWrite, []byte("independent store capacity probe"), 0600), syscall.ENOSPC)
		nix, err := exec.LookPath("nix")
		if os.Getenv("C2J_REQUIRE_TOOL_DISK_NIX") == "1" {
			require.NoError(t, err)
		}
		t.Setenv("C2J_TEST_REAL_NIX", nix)
		t.Setenv("C2J_TEST_NIX_EVAL_ERROR", "builtins.throw "+fmt.Sprintf("%q", diagnostic))
	} else if mode != "nix wrong store" {
		t.Setenv("C2J_TEST_NIX_OUTPUT", diskNixFixtureOutput(t))
	}
	require.NoError(t, os.WriteFile(filepath.Join(bin, "nix"), []byte(`#!/bin/sh
set -eu
case " $* " in
  *' config show store '*)
    if [ "$C2J_TEST_NIX_FAILURE" = "nix wrong store" ]; then
      printf 'local://?root=%s\n' "$C2J_TEST_NIX_STORE_ROOT"
    else
      echo local
    fi ;;
  ' __dump-cli ')
    if [ "$C2J_TEST_NIX_FAILURE" = "nix directory overrides" ]; then
      store="$NIX_STORE_DIR"; state="$NIX_STATE_DIR"
    else
      store=/nix/store; state=/nix/var/nix
    fi
    printf '{"stores":{"Local Store":{"settings":{"root":{"value":null},"store":{"value":"%s"},"real":{"value":"%s"},"state":{"value":"%s"},"log":{"value":"/nix/var/log/nix"}}}}}\n' "$store" "$store" "$state" ;;
  *' flake prefetch '*)
    touch "$(dirname "$0")/nix-prefetch-ran"
    case "$C2J_TEST_NIX_FAILURE" in
      ""|"nix eval full") exit 0 ;;
    esac
    write_error=$(dd if=/dev/zero of="$C2J_TEST_NIX_STORE_WRITE" bs=1 count=1 2>&1) && exit 70
    case "$write_error" in *"No space left on device"*) ;; *) exit 70 ;; esac
    printf '%s\n' "$C2J_TEST_NIX_LOG" >&2
    exit 1 ;;
  *' eval '*)
    if [ "$C2J_TEST_NIX_FAILURE" = "nix eval full" ]; then
      if [ -n "$C2J_TEST_REAL_NIX" ]; then
        exec "$C2J_TEST_REAL_NIX" --extra-experimental-features nix-command \
          --store "local?root=$C2J_TEST_NIX_STORE_ROOT" \
          --log-format internal-json eval --expr "$C2J_TEST_NIX_EVAL_ERROR"
      fi
    fi
    if [ "$C2J_TEST_NIX_FAILURE" = "nix eval full" ] || [ "$C2J_TEST_NIX_FAILURE" = "nix wrong store" ]; then
      printf '%s\n' "$C2J_TEST_NIX_LOG" >&2; exit 1
    fi
    out="$C2J_TEST_NIX_OUTPUT"
    printf '{"drv":"%s-disk-fixture.drv","input":"%s","bin":"%s","main":"","outputs":{"out":"%s"}}\n' "${out%%-*}" "$out" "$out" "$out" ;;
  *' build '*)
    while [ "$1" != --out-link ]; do shift; done
    ln -s "$C2J_TEST_NIX_OUTPUT" "$2" ;;
  *) exit 70 ;;
esac
`), 0700))
	return path, diagnostic
}

func diskNixFixtureOutput(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir("/nix/store")
	if os.IsNotExist(err) && os.Getenv("C2J_REQUIRE_TOOL_DISK_NIX") != "1" {
		t.Skip("Nix worker resume fixture requires an existing store executable")
	}
	require.NoError(t, err)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() && len(name) > 33 && name[32] == '-' && strings.IndexFunc(name[:32], func(char rune) bool {
			return !strings.ContainsRune("0123456789abcdfghijklmnpqrsvwxyz", char)
		}) == -1 {
			root := filepath.Join("/nix/store", name)
			if diskFixtureBin(root) {
				return root
			}
		}
	}
	if os.Getenv("C2J_REQUIRE_TOOL_DISK_NIX") == "1" {
		t.Fatal("Nix worker resume fixture requires an existing store executable")
	}
	t.Skip("Nix worker resume fixture requires an existing store executable")
	return ""
}

func assertDiskWarning(t *testing.T, logs, path, diagnostic string, form bool) {
	t.Helper()
	var warning struct {
		LaunchID   string `json:"launch_id"`
		Task       string `json:"task_type"`
		Ordinal    *int   `json:"chapter_ordinal"`
		Path       string `json:"path"`
		Diagnostic string `json:"diagnostic"`
	}
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, `"event":"task.tool_setup.disk_exhausted"`) {
			require.NoError(t, json.Unmarshal([]byte(line), &warning))
			break
		}
	}
	require.Equal(t, "3700a99b2f3bde156ff465bd73676248", warning.LaunchID)
	require.NotNil(t, warning.Ordinal)
	require.NotContains(t, logs, diskFixtureSecret)
	require.Equal(t, strings.ReplaceAll(path, diskFixtureSecret, "[REDACTED]"), warning.Path)
	require.Contains(t, warning.Diagnostic, strings.ReplaceAll(diagnostic, diskFixtureSecret, "[REDACTED]"))
	if form {
		require.Equal(t, "input:generate_form", warning.Task)
	} else {
		require.Contains(t, warning.Task, "disk_affected:")
	}
}

func diskFixtureBin(root string) bool {
	for _, name := range []string{"include", "lib", "nix-support/setup-hook", "share/pkgconfig"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return false
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "bin"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if stat, err := os.Stat(filepath.Join(root, "bin", entry.Name())); err == nil && !stat.IsDir() && stat.Mode()&0111 != 0 {
			return true
		}
	}
	return false
}
