package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestExtensionWaitFreshRerun(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp(), commandop.GetOp())
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			counter := filepath.Join(t.TempDir(), "runs")
			paths := filepath.Join(t.TempDir(), "paths")
			manifest := fmt.Sprintf(`name: waiting
shell: sh
run: |
  set -eu
  payload=$(cat)
  root=${payload#*\"root\":\"}
  root=${root%%\"*}
  cd "$root"
  test -f earlier || exit 11
  test ! -f discarded || exit 12
  test ! -f ignored || exit 13
  test "$C2J_EXTENSION_WAIT_VERSION" = 1 || exit 14
  printf '%%s\n' "$root" >> %q
  printf '%%s\n' "$C2J_CURRENT_INVOCATION_HASH" >> %q
  if [ "$(wc -l < %q)" -eq 1 ]; then
    printf 'discarded-marker' > discarded
    git add discarded
    git -c user.name=Wait -c user.email=wait@example.test commit -m discarded >/dev/null
    printf 'ignored\n' > .gitignore
    printf 'discarded-marker' > ignored
    printf 'discarded-marker' > "$(dirname "$root")/outbox/discarded"
    printf 'discarded-stderr-marker' >&2
    printf '{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}'
    exit 76
  fi
  printf '{"output":{"ready":true}}'
env: {C2J_EXTENSION_WAIT_VERSION: forged}
input_schema: {type: object, properties: {root: {type: string}}, required: [root]}
output_schema: {type: object, required: [ready]}
`, paths, counter, counter)
			commitHookFile(t, repo, "ops/wait/op.yaml", manifest)
			rt := newTimeoutReproRuntime(t, backend).ConsumerRuntime
			key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
workspace: {cell: test, ref: main}
sequence:
 - id: earlier
   op: command_execution
   inputs: {run: 'printf earlier > earlier', working_directory: '${{ context.environment.op.worktree_path }}'}
 - id: waiting
   op: ./ops/wait
   inputs: {root: '${{ context.environment.op.worktree_path }}'}
   retry: {maximum_attempts: 1}
   catch:
    - when: true
      continue: {outputs: {caught: true, message: '${{ failure.message }}'}}
outputs: {result: '${{ sequence.waiting.outputs }}'}
`)
			allocation := execution.Allocation{SchemaVersion: 2}
			out, err := runNegotiatingRecipe(t, rt, key, allocation, false)
			require.NoError(t, err)
			if out.Status != wf.JobRunSuspended {
				t.Fatalf("wanted suspension, got %s: %v output=%v", out.Status, out.JobError, gitLifecycleOutput(t, out)["result"])
			}
			ctx := celltest.Context(context.Background())
			info, err := rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.Contains(t, string(info.ClientPayload), "extension_wait")
			require.NotContains(t, string(info.ClientPayload), "discarded-marker")
			rawPaths, err := os.ReadFile(paths)
			require.NoError(t, err)
			firstRoot := strings.TrimSpace(string(rawPaths))
			_, err = os.Stat(filepath.Dir(firstRoot))
			require.True(t, os.IsNotExist(err))
			chapters, err := rt.ListChapters(ctx, jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			rawHistory, err := json.Marshal(chapters)
			require.NoError(t, err)
			require.NotContains(t, string(rawHistory), "discarded-stderr-marker")
			require.NotContains(t, string(rawHistory), "discarded-marker")
			require.NotContains(t, string(rawHistory), `"Name":"discarded"`)
			// Pending replay must not execute, register another gate, or publish evidence.
			engine, err := wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId(key.TenantId).PlusWorkers(NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.Error(t, err)
			after, err := rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.Equal(t, info.ClientPayloadRevision, after.ClientPayloadRevision)
			// Actual elapsed-time scheduling, followed by a supplied-lease worker change.
			time.Sleep(1100 * time.Millisecond)
			out, err = runNegotiatingRecipe(t, rt, key, allocation, true)
			require.NoError(t, err)
			result := gitLifecycleOutput(t, out)["result"].(map[string]any)
			require.Equal(t, true, result["ready"])
			log, err := os.ReadFile(counter)
			require.NoError(t, err)
			lines := strings.Fields(string(log))
			require.Len(t, lines, 2)
			require.Equal(t, lines[0], lines[1])
			rawPaths, err = os.ReadFile(paths)
			require.NoError(t, err)
			roots := strings.Fields(string(rawPaths))
			require.Len(t, roots, 2)
			require.NotEqual(t, roots[0], roots[1])
			info, err = rt.GetJob(ctx, key)
			require.NoError(t, err)
			require.NotContains(t, string(info.ClientPayload), "extension_wait")
			require.NoError(t, os.RemoveAll(repo))
			_, err = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			final, err := os.ReadFile(counter)
			require.NoError(t, err)
			require.Equal(t, log, final)
		})
	}
}

func TestExtensionWaitCompilerBeyond64(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "source")
	counter := filepath.Join(t.TempDir(), "runs")
	manifest := fmt.Sprintf(`name: repeated-wait
shell: sh
run: |
  set -eu
  cat >/dev/null
  printf '%%s\n' "$C2J_CURRENT_INVOCATION_HASH" >> %q
  if [ "$(wc -l < %q)" -le 65 ]; then
    printf '{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}'
    exit 76
  fi
  printf '{"output":{"ready":true}}'
input_schema: {type: object}
output_schema: {type: object, required: [ready]}
`, counter, counter)
	commitHookFile(t, repo, "ops/wait/op.yaml", manifest)
	rt := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
	key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
sequence:
 - id: waiting
   op: ./ops/wait
   retry: {maximum_attempts: 1}
   catch:
    - when: true
      continue: {outputs: {caught: true}}
outputs: {result: '${{ sequence.waiting.outputs }}'}
`)
	ctx := celltest.Context(context.Background())
	var firstInvocation string
	for i := 0; i < 65; i++ {
		out, err := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, i%2 == 1)
		require.NoError(t, err)
		require.Equal(t, wf.JobRunSuspended, out.Status, "activation %d: %v", i, out.JobError)
		info, err := rt.GetJob(ctx, key)
		require.NoError(t, err)
		var payload struct {
			C2J struct {
				Gate struct {
					Invocation string    `json:"invocation"`
					Attempt    int       `json:"attempt"`
					NotBefore  time.Time `json:"not_before"`
				} `json:"extension_wait"`
			} `json:"c2j"`
		}
		require.NoError(t, json.Unmarshal(info.ClientPayload, &payload))
		g := payload.C2J.Gate
		require.Equal(t, 1, g.Attempt)
		if i == 0 {
			firstInvocation = g.Invocation
		}
		require.Equal(t, firstInvocation, g.Invocation)
		if d := time.Until(g.NotBefore); d > 0 {
			time.Sleep(d + 20*time.Millisecond)
		}
	}
	out, err := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, false)
	require.NoError(t, err)
	result := gitLifecycleOutput(t, out)["result"].(map[string]any)
	require.Equal(t, true, result["ready"])
	raw, err := os.ReadFile(counter)
	require.NoError(t, err)
	lines := strings.Fields(string(raw))
	require.Len(t, lines, 66)
	for _, line := range lines {
		require.Equal(t, lines[0], line)
	}
}

func TestExtensionWaitSelectorResolution(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, pin := range []bool{false, true} {
		t.Run(map[bool]string{false: "mutable", true: "pinned"}[pin], func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			source := filepath.Join(t.TempDir(), "ops")
			workspaceRepo(t, source, "source")
			counter := filepath.Join(t.TempDir(), "runs")
			manifest := func(version int) string {
				return fmt.Sprintf(`name: selector-wait
shell: sh
run: |
  set -eu
  cat >/dev/null
  printf 'run\n' >> %q
  if [ "$(wc -l < %q)" -eq 1 ]; then
    printf '{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}'
    exit 76
  fi
  printf '{"output":{"ready":true,"source_version":%d}}'
input_schema: {type: object}
output_schema: {type: object, required: [ready]}
`, counter, counter, version)
			}
			commit := commitHookFile(t, source, "ops/wait/op.yaml", manifest(1))
			ref := "main"
			if pin {
				ref = commit
			}
			rt := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
			key := submitGitLifecycle(t, rt, repo, fmt.Sprintf("id: git-lifecycle\nsequence:\n - id: waiting\n   op: git+file://%s//ops/wait@%s\noutputs: {result: '${{ sequence.waiting.outputs }}'}\n", filepath.ToSlash(source), ref))
			out, e := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, false)
			require.NoError(t, e)
			require.Equal(t, wf.JobRunSuspended, out.Status, "%v", out.JobError)
			commitHookFile(t, source, "ops/wait/op.yaml", manifest(2))
			time.Sleep(1100 * time.Millisecond)
			out, e = runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, false)
			require.NoError(t, e)
			result := gitLifecycleOutput(t, out)["result"].(map[string]any)
			want := float64(2)
			if pin {
				want = 1
			}
			require.Equal(t, want, result["source_version"])
		})
	}
}

func TestExtensionWaitManifestTimeout(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	// The manifest budget includes Git/worktree preparation. Allow ordinary
	// startup, then wait beyond that budget before checking the fresh launch.
	const manifestTimeout = 10 * time.Second
	repo := filepath.Join(t.TempDir(), "repo")
	workspaceRepo(t, repo, "source")
	counter := filepath.Join(t.TempDir(), "runs")
	commitHookFile(t, repo, "ops/wait/op.yaml", fmt.Sprintf(`name: bounded-launch
shell: sh
timeout: %s
run: |
  cat >/dev/null
  printf '%%s\n' "$C2J_CURRENT_INVOCATION_HASH" >> %q
  if [ "$(wc -l < %q)" -eq 1 ]; then
    printf '{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}'
    exit 76
  fi
  exec sleep 60
input_schema: {type: object}
output_schema: {type: object}
`, manifestTimeout, counter, counter))
	rt := newTimeoutReproRuntime(t, "sqlite").ConsumerRuntime
	key := submitGitLifecycle(t, rt, repo, `id: git-lifecycle
op: ./ops/wait
retry: {maximum_attempts: 1}
catch:
 - when: true
   continue: {outputs: {caught: true, message: '${{ failure.message }}'}}
`)
	out, err := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, false)
	require.NoError(t, err)
	if out.Status != wf.JobRunSuspended {
		runs, readErr := os.ReadFile(counter)
		var result map[string]any
		if out.Status == wf.JobRunCompleted {
			result = gitLifecycleOutput(t, out)
		}
		t.Fatalf("first launch: wanted SUSPENDED, got %s; job_error=%v; caught=%v; message=%v; runs=%q; read_error=%v", out.Status, out.JobError, result["caught"], result["message"], runs, readErr)
	}
	time.Sleep(manifestTimeout + 100*time.Millisecond)
	started := time.Now()
	out, err = runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, true)
	require.NoError(t, err)
	result := gitLifecycleOutput(t, out)
	require.Equal(t, true, result["caught"])
	require.Equal(t, "Timed out", result["message"])
	require.Less(t, time.Since(started), manifestTimeout+5*time.Second, "manifest timeout must bound the fresh launch")
	raw, err := os.ReadFile(counter)
	require.NoError(t, err)
	lines := strings.Fields(string(raw))
	require.Len(t, lines, 2)
	require.Equal(t, lines[0], lines[1], "wait neither consumed a failure retry nor entered catch")
}

// Keep optional lease authority while injecting a lost reschedule response.
type compilerWaitFaultRuntime struct {
	jobdb.ConsumerRuntime
	after bool
	calls atomic.Int32
}
type compilerWaitFaultLease struct {
	jobdb.ExecutionLease
	owner *compilerWaitFaultRuntime
}
type compilerWaitRenewableLease struct{ *compilerWaitFaultLease }
type compilerWaitDelegatingRuntime struct{ *compilerWaitFaultRuntime }

func (r *compilerWaitFaultRuntime) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.ConsumerRuntime.GetJobLease(ctx, req)
	if err != nil || l == nil {
		return l, err
	}
	return r.wrap(l), nil
}
func (r *compilerWaitFaultRuntime) wrap(l jobdb.ExecutionLease) jobdb.ExecutionLease {
	w := &compilerWaitFaultLease{l, r}
	if _, ok := l.(jobdb.RenewableExecutionLease); ok {
		return &compilerWaitRenewableLease{w}
	}
	return w
}
func (l *compilerWaitFaultLease) Reschedule(ctx context.Context, req jobdb.RescheduleExecutionRequest) error {
	l.owner.calls.Add(1)
	if l.owner.after {
		if err := l.ExecutionLease.Reschedule(ctx, req); err != nil {
			return err
		}
	}
	return errors.New("injected uncertain extension wait handoff")
}
func (l *compilerWaitFaultLease) LeaseToken() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseToken() string }); ok {
		return v.LeaseToken()
	}
	return ""
}
func (l *compilerWaitFaultLease) LeaseWorkerID() string {
	if v, ok := l.ExecutionLease.(interface{ LeaseWorkerID() string }); ok {
		return v.LeaseWorkerID()
	}
	return ""
}
func (l *compilerWaitRenewableLease) LeaseExpiry() time.Time {
	return l.ExecutionLease.(jobdb.RenewableExecutionLease).LeaseExpiry()
}
func (l *compilerWaitRenewableLease) Renew(ctx context.Context) (jobdb.RenewableExecutionLease, error) {
	next, err := l.ExecutionLease.(jobdb.RenewableExecutionLease).Renew(ctx)
	if err != nil {
		return nil, err
	}
	return l.owner.wrap(next).(jobdb.RenewableExecutionLease), nil
}
func (r *compilerWaitDelegatingRuntime) CreateDelegation(ctx context.Context, parent jobdb.ExecutionLease, req jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	switch l := parent.(type) {
	case *compilerWaitFaultLease:
		parent = l.ExecutionLease
	case *compilerWaitRenewableLease:
		parent = l.ExecutionLease
	}
	return r.ConsumerRuntime.(jobdb.LeaseDelegator).CreateDelegation(ctx, parent, req)
}

func TestExtensionWaitUncertainHandoffBypassesCatch(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp(), commandop.GetOp())
	for _, backend := range []string{"toy", "sqlite", "remote"} {
		for _, after := range []bool{false, true} {
			for _, route := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/published=%t/route=%t", backend, after, route), func(t *testing.T) {
					repo := filepath.Join(t.TempDir(), "repo")
					workspaceRepo(t, repo, "source")
					marker := filepath.Join(t.TempDir(), "forbidden")
					commitHookFile(t, repo, "ops/wait/op.yaml", `name: wait
shell: sh
run: 'printf ''{"wait":{"version":1,"until":"2000-01-01T00:00:00Z"}}''; exit 76'
input_schema: {type: object}
output_schema: {type: object}
`)
					base := newTimeoutReproRuntime(t, backend).ConsumerRuntime
					var body string
					if route {
						body = fmt.Sprintf(`id: git-lifecycle
state:
 initial: waiting
 states:
  waiting:
   op: ./ops/wait
   catch:
    - when: true
      to: alternate
  alternate:
   op: command_execution
   inputs: {run: %q}
`, "printf forbidden > "+marker)
					} else {
						body = fmt.Sprintf(`id: git-lifecycle
sequence:
 - id: waiting
   op: ./ops/wait
   catch:
    - when: true
      continue: {outputs: {caught: true}}
 - id: later
   op: command_execution
   inputs: {run: %q}
`, "printf forbidden > "+marker)
					}
					key := submitGitLifecycle(t, base, repo, body)
					fault := &compilerWaitFaultRuntime{ConsumerRuntime: base, after: after}
					var rt jobdb.ConsumerRuntime = fault
					if _, ok := base.(jobdb.LeaseDelegator); ok {
						rt = &compilerWaitDelegatingRuntime{fault}
					}
					out, err := runNegotiatingRecipe(t, rt, key, execution.Allocation{SchemaVersion: 2}, false)
					require.True(t, err != nil || out.Status != wf.JobRunCompleted)
					require.EqualValues(t, 1, fault.calls.Load(), "must stop after uncertain handoff: %v / %v", err, out.JobError)
					_, err = os.Stat(marker)
					require.True(t, os.IsNotExist(err), "catch must not dispatch a later or alternate operation")
					chapters, err := base.ListChapters(celltest.Context(context.Background()), jobdb.ListChaptersRequest{JobKey: key})
					require.NoError(t, err)
					for _, ch := range chapters {
						if b, ok := ch.Body.(jobdb.ExecutionChapter); ok {
							r, err := jobdb.DecodeExecutionRecord(b.Record)
							require.NoError(t, err)
							require.False(t, r.Kind == "result" && strings.Contains(r.ID, "extension_execution"), "uncertain handoff must not commit a failed operation")
						}
					}
				})
			}
		}
	}
}
