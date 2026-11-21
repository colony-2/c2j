package compiler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestArtifactFileRetryAndReopenedReplay(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, kind := range []string{"http", "file"} {
		t.Run(kind, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			repo := t.TempDir()
			workspaceRepo(t, repo, "source")
			trace := filepath.Join(t.TempDir(), "trace")
			var requests atomic.Int32
			first, second := []byte("attempt one bytes"), []byte("attempt two bytes")
			server := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) == 1 {
					_, _ = w.Write(first)
				} else {
					_, _ = w.Write(second)
				}
			})))
			defer server.Close()
			ref := artifacts.NewExternalRef("document", server.URL, false)
			var extra map[string]any
			if kind == "file" {
				path := filepath.Join(t.TempDir(), "source")
				require.NoError(t, os.WriteFile(path, first, 0600))
				ref = artifacts.NewExternalRef("document", "file://"+path, false)
				extra = map[string]any{"env": map[string]string{"C2J_ARTIFACT_FILE_PROBE": "1", "C2J_ARTIFACT_FILE_TRACE": trace, "C2J_ARTIFACT_FILE_MODE": "fail-once", "C2J_ARTIFACT_MUTATE_FILE": path}}
			}
			artifactFileManifest(t, repo, trace, "fail-once", extra)
			source := fmt.Sprintf("id: artifact-files\nop: ./op\nretry: {maximum_attempts: 2, initial_interval: 1ms}\ninputs: {instructions_file: %s}\n", fileJSON(t, ref))
			db := filepath.Join(t.TempDir(), "jobs.db")
			rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: db})
			require.NoError(t, err)
			closed := false
			defer func() {
				if !closed {
					require.NoError(t, rt.Close(ctx))
				}
			}()
			key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil)
			out, err := runGitLifecycle(t, rt, key)
			require.NoError(t, err)
			result := gitLifecycleOutput(t, out)
			assertFileDigest(t, result, "instructions_file", second)
			records := fileTrace(t, trace)
			require.Len(t, records, 2)
			require.NotEqual(t, records[0]["instructions_file"].(map[string]any)["path"], records[1]["instructions_file"].(map[string]any)["path"])
			assertFileDigest(t, records[0]["_probe_digests"].(map[string]any), "instructions_file", first)
			assertFileDigest(t, records[1]["_probe_digests"].(map[string]any), "instructions_file", second)
			assertFileCleanup(t, records)
			for _, record := range records {
				require.JSONEq(t, fileJSON(t, ref), fileJSON(t, record["instructions_file"].(map[string]any)["ref"]))
			}
			if kind == "http" {
				require.EqualValues(t, 2, requests.Load())
			}
			durable := assertFileDurableValues(t, rt, key, "artifact-files-", string(first), string(second))
			require.Contains(t, durable, ref.External.URL)
			require.NoError(t, rt.Close(ctx))
			closed = true
			require.NoError(t, os.RemoveAll(repo))
			if kind == "file" {
				require.NoError(t, os.Remove(strings.TrimPrefix(ref.External.URL, "file://")))
			}
			server.Close()
			// Replace the worker and its database connection; remove source/tools too.
			fresh, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: db})
			require.NoError(t, err)
			defer fresh.Close(ctx)
			t.Setenv("PATH", t.TempDir())
			worker := NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})
			engine, err := jobworkflow.NewEngineBuilder().WithRuntime(fresh).WithWorkerTenantId(key.TenantId).PlusWorkers(worker).BuildEngine()
			require.NoError(t, err)
			replay, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: worker})
			require.NoError(t, err)
			raw, err := replay.GetData()
			require.NoError(t, err)
			require.JSONEq(t, fileJSON(t, result), string(raw))
			require.Len(t, fileTrace(t, trace), 2)
			if kind == "http" {
				require.EqualValues(t, 2, requests.Load())
			}
		})
	}
}

func TestArtifactFileConsumerFailureCleanup(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	repo := t.TempDir()
	workspaceRepo(t, repo, "source")
	trace := filepath.Join(t.TempDir(), "trace")
	artifactFileManifest(t, repo, trace, "fail", nil)
	body := []byte("failure fixture body")
	rt := newTimeoutReproRuntime(t, "sqlite")
	source := `id: artifact-files
op: ./op
retry: {maximum_attempts: 1}
inputs: {instructions_file: '${{ context.artifacts["document"] }}'}
`
	key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil, jobdb.NewArtifactFromBytes("document", body))
	out, err := runGitLifecycle(t, rt, key)
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunFailed, out.Status)
	assertFileCleanup(t, fileTrace(t, trace))
	assertFileDurableValues(t, rt, key, "artifact-files-", string(body))
}

func findArtifactStaging(root string) []string {
	var paths []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() && strings.HasPrefix(entry.Name(), "artifact-files-") {
			paths = append(paths, path)
			return filepath.SkipDir
		}
		return nil
	})
	return paths
}

func TestArtifactFileStagingDeadlineAndCancellation(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	for _, mode := range []string{"deadline", "task deadline", "cancel staging", "cancel consumer"} {
		t.Run(mode, func(t *testing.T) {
			scratch, scratchErr := os.MkdirTemp("/tmp", "af-")
			require.NoError(t, scratchErr)
			t.Cleanup(func() { _ = os.RemoveAll(scratch) })
			t.Setenv("TMPDIR", scratch)
			repo := t.TempDir()
			workspaceRepo(t, repo, "source")
			trace := filepath.Join(t.TempDir(), "trace")
			ctx, cancel := context.WithTimeout(celltest.Context(context.Background()), 15*time.Second)
			defer cancel()
			staged := make(chan []string, 1)
			server := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "cancel consumer" {
					_, _ = w.Write([]byte("consumer bytes"))
					return
				}
				staged <- findArtifactStaging(scratch)
				if mode == "cancel staging" {
					cancel()
				}
				<-r.Context().Done()
			})))
			defer server.Close()
			extra := map[string]any{}
			probeMode := ""
			if mode == "deadline" {
				extra["timeout"] = "3s"
			}
			if mode == "cancel consumer" {
				probeMode = "wait"
			}
			artifactFileManifest(t, repo, trace, probeMode, extra)
			rt := newTimeoutReproRuntime(t, "sqlite")
			source := fmt.Sprintf("id: artifact-files\nop: ./op\nretry: {maximum_attempts: 1}\ninputs: {instructions_file: %s}\n", fileJSON(t, artifacts.NewExternalRef("x", server.URL, false)))
			if mode == "task deadline" {
				source += "timeout: 4s\n"
			}
			key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil)
			watcherDone := make(chan struct{})
			if mode == "cancel consumer" {
				go func() {
					defer close(watcherDone)
					for {
						if _, err := os.Stat(trace); err == nil {
							cancel()
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(5 * time.Millisecond):
						}
					}
				}()
			} else {
				close(watcherDone)
			}
			bounded, stop := context.WithTimeout(ctx, 15*time.Second)
			defer stop()
			out, err := runGitLifecycleContext(t, bounded, rt, key)
			t.Logf("cancellation outcome status=%s error=%v job_error=%v", out.Status, err, out.JobError)
			if mode == "deadline" || mode == "task deadline" {
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunFailed, out.Status)
				if mode == "deadline" {
					// Upstream compact execution failures expose the timeout summary.
					require.EqualError(t, out.JobError, "Timed out")
				} else {
					var timeout *jobdb.TimeoutError
					require.ErrorAs(t, out.JobError, &timeout)
				}
			} else {
				require.True(t, err != nil || out.JobError != nil || out.Status != jobworkflow.JobRunCompleted)
			}
			<-watcherDone
			// JobDB can return on cancellation before the in-flight task unwinds.
			require.Eventually(t, func() bool { return len(findArtifactStaging(scratch)) == 0 }, 3*time.Second, 5*time.Millisecond, "cooperative task cleanup")
			if mode == "cancel consumer" {
				assertFileCleanup(t, fileTrace(t, trace))
			} else {
				select {
				case paths := <-staged:
					require.Len(t, paths, 1)
					for _, path := range paths {
						require.NoDirExists(t, path)
					}
				case <-time.After(time.Second):
					t.Fatal("staging never reached HTTP request")
				}
				assertFileLaunches(t, trace, 0)
				require.NoFileExists(t, trace)
			}
			require.Empty(t, findArtifactStaging(scratch))
			assertFileDurableValues(t, rt, key, "artifact-files-", "consumer bytes")
		})
	}
}

func TestArtifactFileSecondInputFailureCleanup(t *testing.T) {
	withRegisteredOps(t, extops.GetExecutionOp())
	scratch, err := os.MkdirTemp("/tmp", "af-partial-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	t.Setenv("TMPDIR", scratch)
	repo := t.TempDir()
	workspaceRepo(t, repo, "source")
	trace := filepath.Join(t.TempDir(), "trace")
	artifactFileManifest(t, repo, trace, "", nil)
	type stagedFirst struct {
		paths []string
		body  []byte
		err   error
	}
	observed := make(chan stagedFirst, 1)
	first := []byte("partial-first-bytes")
	server := httptest.NewServer(celltest.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			_, _ = w.Write(first)
			return
		}
		paths := findArtifactStaging(scratch)
		observation := stagedFirst{paths: paths}
		if len(paths) == 1 {
			observation.body, observation.err = os.ReadFile(filepath.Join(paths[0], "000000"))
		}
		observed <- observation
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("truncated"))
	})))
	defer server.Close()
	source := fmt.Sprintf("id: artifact-files\nop: ./op\nretry: {maximum_attempts: 1}\ninputs: {instructions_file: %s, prompt_prefix_file: %s}\n", fileJSON(t, artifacts.NewExternalRef("first", server.URL+"/first", false)), fileJSON(t, artifacts.NewExternalRef("second", server.URL+"/second", false)))
	rt := newTimeoutReproRuntime(t, "sqlite")
	key := submitArtifactFileRecipe(t, rt, repo, source, "file-tests", "", nil)
	out, err := runGitLifecycle(t, rt, key)
	require.NoError(t, err)
	require.Equal(t, jobworkflow.JobRunFailed, out.Status)
	require.ErrorContains(t, out.JobError, "inputs.prompt_prefix_file")
	select {
	case observation := <-observed:
		require.NoError(t, observation.err)
		require.Equal(t, first, observation.body)
		require.Len(t, observation.paths, 1)
		for _, path := range observation.paths {
			require.NoDirExists(t, path)
		}
	case <-time.After(time.Second):
		t.Fatal("did not reach second staging source")
	}
	assertFileLaunches(t, trace, 0)
	require.NoFileExists(t, trace)
	assertFileDurableValues(t, rt, key, "artifact-files-", string(first))
}
