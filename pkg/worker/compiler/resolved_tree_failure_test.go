package compiler

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/pathutil"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// Fail a real artifact upload or lose the worker before the successful outcome
// is durable. Neither can make the locally adopted pair the accepted snapshot.
type resolvedOutcomeFault struct {
	jobdb.WorkflowRuntime
	lease    jobdb.ExecutionLease
	artifact bool
	selector string
	fired    bool
	cancel   context.CancelFunc
}

func (r *resolvedOutcomeFault) GetJobLease(ctx context.Context, req jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	l, err := r.WorkflowRuntime.GetJobLease(ctx, req)
	r.lease = l
	return l, err
}

func (r *resolvedOutcomeFault) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if body, ok := req.Chapter.Body.(jobdb.ExecutionChapter); ok && !r.fired {
		record, err := jobdb.DecodeExecutionRecord(body.Record)
		if err != nil {
			return err
		}
		selector := r.selector
		if selector == "" {
			selector = "rebase"
		}
		if record.Kind == "result" && strings.Contains(record.ID, `scope:"op:integrate"`) && strings.Contains(record.ID, `task:"`+selector+`:`+selector+`"`) {
			if record.ErrorKind != "" {
				return fmt.Errorf("expected a successful prepared adoption, got %s", record.ErrorKind)
			}
			if r.artifact {
				found := false
				for i := range req.ArtifactUploads {
					if req.ArtifactUploads[i].Name == gitstate.ThinPackArtifactName {
						found = true
						req.ArtifactUploads[i].Open = func() (io.ReadCloser, error) { return nil, fmt.Errorf("injected adoption artifact upload failure") }
					}
				}
				if !found {
					return fmt.Errorf("adoption did not prepare a replacement snapshot")
				}
				if err := r.WorkflowRuntime.PutChapter(ctx, req); err == nil {
					return fmt.Errorf("artifact upload failure was incorrectly accepted")
				}
			}
			r.fired = true
			if err := r.lease.Reschedule(ctx, jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}); err != nil {
				return err
			}
			r.cancel()
			return fmt.Errorf("injected worker loss before durable adoption outcome")
		}
	}
	return r.WorkflowRuntime.PutChapter(ctx, req)
}

func TestResolvedTreeUnrecordedOutcomeRestoresPriorAcceptedPair(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, failure := range []string{"outcome", "artifact-upload"} {
			t.Run(backend+"/"+failure, func(t *testing.T) {
				repo, base, target := resolvedFixture(t)
				calls := 0
				var prior map[string]any
				var previousDir string
				op := resolvedCountedOp{thinpackrebase.GetRebaseOp(), func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
					calls++
					before := resolvedObserve(t, d, ctx)
					require.Equal(t, base, before["base"])
					require.Equal(t, input["persist_hash"], before["head"])
					if prior != nil {
						require.Equal(t, prior["head"], before["head"])
						require.Equal(t, prior["tree"], before["tree"])
						require.Equal(t, prior["base"], before["base"])
						require.NotEqual(t, previousDir, d.WorktreePath())
					}
					prior, previousDir = before, d.WorktreePath()
					return native(d, ctx, input)
				}}
				workspaceTestWorker(t, append(resolvedFixtureOps(t, target, false), op)...)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, resolvedRecipe("rebase", target, "direct"))
				fault := &resolvedOutcomeFault{WorkflowRuntime: rt, artifact: failure == "artifact-upload"}
				_, err := runGitLifecycle(t, fault, key)
				require.Error(t, err)
				require.True(t, fault.fired)
				require.Equal(t, 1, calls)
				require.NoError(t, os.RemoveAll(previousDir))
				// No successful adoption result exists before replacement starts.
				history, err := jobdb.GetJobRun(celltest.Context(context.Background()), rt, jobdb.GetJobRunRequest{JobKey: key, IncludeOutputs: true, IncludeArtifacts: true})
				require.NoError(t, err)
				for _, entry := range history.Execution {
					if entry.Record.Kind == "result" && strings.Contains(entry.Record.ID, `task:"rebase:rebase"`) {
						t.Fatal("unrecorded adoption acquired a durable result")
					}
				}
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				result := gitLifecycleOutput(t, out)
				restored := result["restored"].(map[string]any)
				require.Equal(t, target, restored["base"])
				require.Equal(t, prior["tree"], restored["tree"])
				require.Equal(t, 2, calls, "replacement retries only the unaccepted adoption")
			})
		}
	}
}

func TestResolvedTreePersistenceFailureRetainsRepairSnapshot(t *testing.T) {
	// Exercise the temporary-directory aliases used on macOS on every platform.
	// Keep the prefix short enough for the Git credential broker's Unix socket.
	realTemp, err := os.MkdirTemp("/tmp", "c2j-rt-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(realTemp)) })
	aliasedTemp := filepath.Join(realTemp, "alias")
	require.NoError(t, os.Symlink(realTemp, aliasedTemp))
	t.Setenv("TMPDIR", aliasedTemp)

	for _, backend := range []string{"sqlite", "remote"} {
		for _, failure := range []string{"base-setting-unavailable", "pack", "diff"} {
			t.Run(backend+"/"+failure, func(t *testing.T) {
				repo, base, target := resolvedFixture(t)
				realGit, err := exec.LookPath("git")
				require.NoError(t, err)
				marker := filepath.Join(t.TempDir(), "adoption-worktree")
				shim := t.TempDir()
				script := `#!/bin/sh
if test -f "$C2J_RESOLVED_MARKER" && test "$(cat "$C2J_RESOLVED_MARKER")" = "$(pwd -P)"; then
 case "$C2J_RESOLVED_FAILURE:$1:$2" in
  pack:bundle:create|diff:diff:--unified=10) printf 'injected adoption %s failure\n' "$C2J_RESOLVED_FAILURE" >&2; exit 73;;
 esac
fi
exec "$C2J_RESOLVED_GIT" "$@"
`
				require.NoError(t, os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0755))
				t.Setenv("C2J_RESOLVED_MARKER", marker)
				t.Setenv("C2J_RESOLVED_FAILURE", failure)
				t.Setenv("C2J_RESOLVED_GIT", realGit)
				t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
				var installed map[string]any
				op := resolvedCountedOp{thinpackrebase.GetRebaseOp(), func(d coreops.OpDependencies, ctx context.Context, input map[string]any, native func(coreops.OpDependencies, context.Context, map[string]any) (map[string]any, error)) (map[string]any, error) {
					if failure == "base-setting-unavailable" {
						return native(struct{ coreops.OpDependencies }{d}, ctx, input)
					}
					result, err := native(d, ctx, input)
					if err == nil {
						installed = resolvedObserve(t, d, ctx)
						worktree, err := pathutil.CanonicalExisting(d.WorktreePath())
						require.NoError(t, err)
						require.NoError(t, os.WriteFile(marker, []byte(worktree), 0600))
					}
					return result, err
				}}
				workspaceTestWorker(t, append(resolvedFixtureOps(t, target, false), op)...)
				body := resolvedRecipe("rebase", target, "direct")
				body = strings.Replace(body, " - id: observe", `   catch:
    - when: true
      continue:
       outputs: {failure: '${{ failure }}'}
 - id: observe`, 1)
				rt := newTimeoutReproRuntime(t, backend)
				key := submitGitLifecycle(t, rt, repo, body)
				out, err := runGitLifecycle(t, rt, key)
				require.NoError(t, err)
				result := gitLifecycleOutput(t, out)
				prepared := result["prepared"].(map[string]any)
				restored := result["restored"].(map[string]any)
				integration, ok := result["integration"].(map[string]any)
				require.True(t, ok, "expected integration output, got %v", result["integration"])
				failureResult, ok := integration["failure"].(map[string]any)
				require.True(t, ok, "fault injection did not produce a caught failure: %s", resolvedJSON(t, integration))
				require.NotEqual(t, thinpackrebase.ConflictCode, failureResult["code"])
				require.Equal(t, base, restored["base"])
				require.Equal(t, prepared["head"], restored["head"])
				require.Equal(t, prepared["tree"], restored["tree"])
				if failure != "base-setting-unavailable" {
					require.NotNil(t, installed)
					require.NotEqual(t, installed["head"], restored["head"], "locally installed adoption is not accepted")
					require.Contains(t, resolvedJSON(t, failureResult), "injected adoption "+failure+" failure")
				}
			})
		}
	}
}
