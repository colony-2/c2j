package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestExtensionEAGAINSQLite(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp(), commandop.GetOp())
	for _, name := range []string{"abandon", "persist", "const", "mixed", "unchanged", "reset-to-base", "dispatched", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			counter := filepath.Join(t.TempDir(), "runs")
			mutable := filepath.Join(t.TempDir(), "source.txt")
			require.NoError(t, os.WriteFile(mutable, []byte("original bytes"), 0o600))
			mode, patch, constant := "persist", `,"next_inputs":{"cursor":"second"}`, "false"
			if name == "abandon" {
				mode, patch = "abandon", ""
			}
			if name == "const" {
				constant = "true"
			}
			manifest := fmt.Sprintf(`name: repeat
shell: sh
run: |
  set -eu
  payload=$(cat)
  root=${payload#*\"root\":\"}
  root=${root%%%%\"*}
  cd "$root"
  inbox=$(dirname "$root")/inbox
  outbox=$(dirname "$root")/outbox
  test "$C2J_EXTENSION_EAGAIN_VERSION" = 1
  test ! -e advanced
  test "$(cat "$inbox/source.txt")" = 'original bytes'
  printf '%%s\n' "$root" >> %q
  launch=$(wc -l < %q)
  scenario=%q
  if [ "$launch" -eq 1 ]; then
    test -z "${C2J_EXTENSION_EAGAIN_RESUME+x}"
    echo 'mutated by extension' > "$inbox/source.txt"
    mkdir "$outbox/nested"
    if [ "$scenario" = reset-to-base ]; then
      git rev-parse HEAD > "$outbox/nested/base"
    fi
    if [ "$scenario" != unchanged ]; then
      echo checkpoint > committed
      git add committed
      git -c user.name=EAGAIN -c user.email=test@example.test commit -m checkpoint >/dev/null
      echo carried > carried
    fi
    echo overwrite > "$outbox/nested/keep"
    echo remove > "$outbox/nested/delete"
    printf '%%s' '{"eagain":{"version":1,"mode":"%s"%s}}'
    exit 77
  fi
  case "$C2J_EXTENSION_EAGAIN_RESUME" in
    *'"version":1'*) ;;
    *) exit 31 ;;
  esac
  if [ "$scenario" = abandon ]; then
    test ! -e carried
    test ! -e committed
    test ! -e "$outbox/nested"
    case "$payload" in *'"cursor":"first"'*) ;; *) exit 32 ;; esac
  elif [ "$scenario" = mixed ] && [ "$launch" -eq 2 ]; then
    test "$(cat carried)" = carried
    test "$(cat committed)" = checkpoint
    test "$(cat "$outbox/nested/keep")" = overwrite
    echo discarded > carried
    echo discarded > "$outbox/nested/keep"
    printf '%%s' '{"eagain":{"version":1,"mode":"abandon"}}'
    exit 77
  elif [ "$scenario" = reset-to-base ]; then
    base=$(cat "$outbox/nested/base")
    if [ "$launch" -eq 2 ]; then
      test "$(cat carried)" = carried
      test "$(cat committed)" = checkpoint
      test "$(cat "$outbox/nested/keep")" = overwrite
      git reset --hard "$base" >/dev/null
      test -z "$(git status --porcelain)"
      rm "$outbox/nested/delete"
      echo replacement > "$outbox/nested/keep"
      echo added > "$outbox/nested/new"
      printf '%%s' '{"eagain":{"version":1,"mode":"persist","next_inputs":{"cursor":"third"}}}'
      exit 77
    fi
    test "$launch" -eq 3
    case "$payload" in *'"cursor":"third"'*) ;; *) exit 34 ;; esac
    test "$(git rev-parse HEAD)" = "$base"
    test -z "$(git status --porcelain)"
    test ! -e carried
    test ! -e committed
    test ! -e "$outbox/nested/delete"
    test "$(cat "$outbox/nested/keep")" = replacement
    test "$(cat "$outbox/nested/new")" = added
  else
    case "$payload" in *'"cursor":"second"'*) ;; *) exit 33 ;; esac
    case "$scenario" in
      const|unchanged) test ! -e carried && test ! -e committed ;;
      *) test "$(cat carried)" = carried && test "$(cat committed)" = checkpoint ;;
    esac
    if [ "$scenario" = mixed ]; then
      test ! -e "$outbox/nested"
    else
      test "$(cat "$outbox/nested/keep")" = overwrite
      rm "$outbox/nested/delete"
      echo final > "$outbox/nested/keep"
    fi
  fi
  printf '%%s' '{"output":{"eagain":"business result"}}'
env: {C2J_EXTENSION_EAGAIN_VERSION: forged, C2J_EXTENSION_EAGAIN_RESUME: forged}
input_schema: {type: object, required: [root, cursor]}
output_schema: {type: object, required: [eagain]}
`, counter, counter, name, mode, patch)
			commitHookFile(t, repo, "ops/repeat/op.yaml", manifest)
			runtime := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
			dispatched := name == "dispatched"
			bulk := ""
			if dispatched {
				bulk = strings.Repeat("x", 140*1024)
			}
			key := submitGitLifecycle(t, runtime, repo, fmt.Sprintf(`id: git-lifecycle
workspace: {cell: test, ref: main}
sequence:
 - id: repeat
   op: ./ops/repeat
   const: %s
   retry: {maximum_attempts: 1}
   inputs: {root: '${{ context.environment.op.worktree_path }}', cursor: first, bulk: %q}
   artifacts: {source.txt: {kind: external, name: source.txt, external: {url: %q}}}
 - id: later
   op: command_execution
   inputs: {working_directory: '${{ context.environment.op.worktree_path }}', run: 'if test -e advanced; then printf advanced; else printf original; fi'}
outputs: {result: '${{ sequence.repeat.outputs }}', later: '${{ sequence.later.outputs.stdout }}'}
`, constant, bulk, "file://"+mutable))
			out, err := runEagainRecipe(t, ctx, runtime, key, dispatched, false)
			if dispatched {
				require.NoError(t, err)
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				wait, err := runtime.GetJob(ctx, key)
				require.NoError(t, err)
				require.NotNil(t, wait.ExecutionState.TaskWait)
				taskOutcome, err := runEagainRecipe(t, ctx, runtime, key, false, true)
				require.NoError(t, err)
				require.Equal(t, wf.JobRunSuspended, taskOutcome.Status, "%v", taskOutcome.JobError)
				require.NotNil(t, taskOutcome.Handoff)
				wait, err = runtime.GetJob(ctx, key)
				require.NoError(t, err)
				require.Nil(t, wait.ExecutionState.TaskWait)
				require.Nil(t, wait.ExecutionState.ResourceResume)
				out, err = runEagainRecipe(t, ctx, runtime, key, false, false)
				require.NoError(t, err)
			}
			require.NoError(t, err)
			require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
			parked, err := runtime.GetJob(ctx, key)
			require.NoError(t, err)
			require.NotNil(t, parked.ExecutionState.ResourceResume)
			lease, err := runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, WorkerID: "too-early", Routes: []jobdb.Route{{JobType: "recipe"}}})
			require.NoError(t, err)
			require.Nil(t, lease)
			if name == "cancelled" {
				require.NoError(t, runtime.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: key, Reason: "cancel while backed off"}))
				outcome, err := runEagainRecipe(t, ctx, runtime, key, false, false)
				require.NoError(t, err)
				require.NotEqual(t, wf.JobRunCompleted, outcome.Status)
				paths, err := os.ReadFile(counter)
				require.NoError(t, err)
				require.Len(t, strings.Fields(string(paths)), 1)
				return
			}
			require.NoError(t, os.WriteFile(mutable, []byte("changed live bytes"), 0o600))
			commitHookFile(t, repo, "advanced", "new tip\n")
			waitForCompilerJobReady(t, runtime, key, 1, time.Sleep)
			out, err = runEagainRecipe(t, ctx, runtime, key, dispatched, false)
			require.NoError(t, err)
			if dispatched {
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				out, err = runEagainRecipe(t, ctx, runtime, key, false, true)
				require.NoError(t, err)
			}
			wantLaunches := 2
			if name == "mixed" || name == "reset-to-base" {
				require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
				waitForCompilerJobReady(t, runtime, key, 2, time.Sleep)
				out, err = runEagainRecipe(t, ctx, runtime, key, false, false)
				require.NoError(t, err)
				wantLaunches = 3
			}
			outputs := gitLifecycleOutput(t, out)
			require.Equal(t, map[string]any{"eagain": "business result"}, outputs["result"])
			later := "advanced"
			if name == "persist" || name == "mixed" || dispatched {
				later = "original"
			}
			require.Equal(t, later, outputs["later"])
			paths, err := os.ReadFile(counter)
			require.NoError(t, err)
			list := strings.Fields(string(paths))
			require.Len(t, list, wantLaunches)
			for _, next := range list[1:] {
				require.NotEqual(t, list[0], next)
			}
			replay := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})
			engine, err := wf.NewEngineBuilder().WithRuntime(runtime).WithWorkerTenantId(key.TenantId).PlusWorkers(replay).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key, JobWorker: replay})
			require.NoError(t, err)
			unchanged, err := os.ReadFile(counter)
			require.NoError(t, err)
			require.Equal(t, paths, unchanged)
		})
	}
}

func runEagainRecipe(t *testing.T, ctx context.Context, runtime jobdb.ConsumerRuntime, key jobdb.JobKey, omitExtension, pending bool) (wf.JobRunOutcome, error) {
	t.Helper()
	return runEagainRecipeAs(t, ctx, runtime, key, omitExtension, pending, "eagain", time.Minute)
}

func runEagainRecipeAs(t *testing.T, ctx context.Context, runtime jobdb.ConsumerRuntime, key jobdb.JobKey, omitExtension, pending bool, workerID string, leaseDuration time.Duration) (wf.JobRunOutcome, error) {
	t.Helper()
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	engine, err := wf.NewEngineBuilder().WithRuntime(runtime).BuildEngine()
	require.NoError(t, err)
	guard := executionruntime.New(runtime, execution.Allocation{SchemaVersion: 2})
	deps := coreops.NewServiceDepsBuilder().WithWorkflowControl(&workerworkflow.SWFWorkflowControl{Engine: engine}).Build()
	workset, err := NewRecipeWorkerWithOptions(deps, registry, RecipeJobWorkerOptions{StageExecution: guard.Stage, WrapTaskWorker: guard.WrapTaskWorker, ContinuationRoute: guard.ContinuationRoute})
	require.NoError(t, err)
	var tasks []wf.TaskWorker
	for _, task := range workset.TaskWorkers {
		if !omitExtension || !strings.HasPrefix(task.Name(), extops.ExecutionOpType+":") {
			tasks = append(tasks, task)
		}
	}
	request := wf.GetJobForRunRequest{JobKey: key, JobWorker: workset.JobWorker, TaskWorkers: tasks, WorkerID: workerID, LeaseDuration: leaseDuration, AwaitThreshold: time.Millisecond}
	if pending {
		state, err := runtime.GetJob(ctx, key)
		require.NoError(t, err)
		require.NotNil(t, state.NextRoute)
		lease, err := runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: key, Routes: []jobdb.Route{*state.NextRoute}, WorkerID: request.WorkerID, LeaseDuration: request.LeaseDuration})
		require.NoError(t, err)
		require.NotNil(t, lease)
		return guard.RunWithLease(ctx, lease, request, nil)
	}
	runnable, err := wf.GetJobForRun(ctx, guard, request)
	if err != nil {
		return wf.JobRunOutcome{}, err
	}
	return runnable.Run(nil)
}
