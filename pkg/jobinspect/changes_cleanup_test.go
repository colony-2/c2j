//go:build unix

package jobinspect_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type arrivingChangesRuntime struct {
	jobdb.ConsumerRuntime
	started chan struct{}
	closed  chan struct{}
}

// SIGKILL delivery and descendant exit are asynchronous. Wait reaps the direct
// child, but a descendant can still appear runnable while finishing its exit.
// Require a terminal observation within a bound; a live survivor must still fail.
func waitChangesProcessExit(ctx context.Context, pid string) (string, error) {
	var state string
	for {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		raw, err := exec.CommandContext(ctx, "ps", "-o", "stat=", "-p", pid).Output()
		observed := strings.TrimSpace(string(raw))
		if err != nil {
			// ps exits 1 when the requested process no longer exists. Other
			// failures, including an unavailable ps, are not proof of cleanup.
			var exit *exec.ExitError
			if ctx.Err() != nil {
				return state, ctx.Err()
			}
			if observed != "" || !errors.As(err, &exit) || exit.ExitCode() != 1 || len(exit.Stderr) != 0 {
				return state, err
			}
		}
		state = observed
		if state == "" || strings.HasPrefix(state, "Z") {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return state, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func requireChangesProcessExit(t *testing.T, pid []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state, err := waitChangesProcessExit(ctx, strings.TrimSpace(string(pid)))
	require.NoError(t, err, "process %s did not stop; last observed state: %q", pid, state)
}

func TestChangesProcessExitObservation(t *testing.T) {
	process := exec.Command("sleep", "30")
	require.NoError(t, process.Start())
	t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
	pid := fmt.Sprint(process.Process.Pid)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := waitChangesProcessExit(ctx, pid)
	require.ErrorIs(t, err, context.DeadlineExceeded, "a live survivor must fail the cleanup check")
	require.NoError(t, process.Process.Kill())
	requireChangesProcessExit(t, []byte(pid)) // A zombie is stopped even before Wait reaps it.
	require.Error(t, process.Wait())
	requireChangesProcessExit(t, []byte(pid)) // The reaped process is absent.
	t.Setenv("PATH", t.TempDir())
	_, err = waitChangesProcessExit(context.Background(), pid)
	require.ErrorIs(t, err, exec.ErrNotFound, "a failed probe must not be mistaken for cleanup")
}

type arrivingChangesArtifact struct {
	jobdb.ArtifactReader
	started, closed chan struct{}
}
type arrivingChangesBody struct {
	io.ReadCloser
	started, closed chan struct{}
	once            sync.Once
	first           bool
}

func (r *arrivingChangesRuntime) OpenArtifact(ctx context.Context, q jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	a, e := r.ConsumerRuntime.OpenArtifact(ctx, q)
	if e != nil || q.Name != gitstate.ThinPackArtifactName {
		return a, e
	}
	return arrivingChangesArtifact{a, r.started, r.closed}, nil
}
func (a arrivingChangesArtifact) Open() (io.ReadCloser, error) {
	r, e := a.ArtifactReader.Open()
	if e != nil {
		return nil, e
	}
	return &arrivingChangesBody{ReadCloser: r, started: a.started, closed: a.closed, first: true}, nil
}
func (b *arrivingChangesBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.first {
		b.first = false
		n, e := b.ReadCloser.Read(p[:1])
		close(b.started)
		return n, e
	}
	<-b.closed
	return 0, os.ErrClosed
}
func (b *arrivingChangesBody) Close() error {
	var e error
	b.once.Do(func() { e = b.ReadCloser.Close(); close(b.closed) })
	return e
}

func TestChangesJ32L08CancelArrivingArtifact(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{})
			wrapped := &arrivingChangesRuntime{ConsumerRuntime: rt, started: make(chan struct{}), closed: make(chan struct{})}
			tmp := t.TempDir()
			api := changesAPI(t, wrapped, jobinspect.ChangesConfig{TempDir: tmp})
			cancelCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			result := make(chan error, 1)
			go func() { _, e := api.History(cancelCtx, jobinspect.ChangesRequest{JobID: f.Job.JobId}); result <- e }()
			select {
			case <-wrapped.started:
			case <-time.After(5 * time.Second):
				t.Fatal("artifact bytes never arrived")
			}
			cancel()
			select {
			case e := <-result:
				require.ErrorIs(t, e, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("canceled download did not release its reader")
			}
			select {
			case <-wrapped.closed:
			default:
				t.Fatal("artifact stream was not closed")
			}
			files, e := os.ReadDir(tmp)
			require.NoError(t, e)
			require.Empty(t, files)
		})
	}
}

func TestChangesJ32L07GitProcessGroupCleanup(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{})
			dir := t.TempDir()
			// The wrapper starts a real descendant and blocks. The reader must kill
			// both processes, wait for its child, and remove partial initialization.
			wrapper := filepath.Join(dir, "git-blocked")
			require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\necho $$ > \""+dir+"/parent\"\nsleep 30 &\necho $! > \""+dir+"/child\"\nwait\n"), 0700))
			tmp := t.TempDir()
			api := changesAPI(t, rt, jobinspect.ChangesConfig{GitPath: wrapper, TempDir: tmp, Limits: jobinspect.ChangesLimits{GitTime: 200 * time.Millisecond}})
			_, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.ErrorIs(t, e, context.DeadlineExceeded)
			for _, name := range []string{"parent", "child"} {
				pid, e := os.ReadFile(filepath.Join(dir, name))
				require.NoError(t, e)
				requireChangesProcessExit(t, pid)
			}
			files, e := os.ReadDir(tmp)
			require.NoError(t, e)
			require.Empty(t, files)
		})
	}
}

func TestChangesJ30J32GitDiagnosticIdentity(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			for _, command := range []string{"rev-parse", "merge-base"} {
				for _, deadline := range []bool{false, true} {
					name := "cancel-" + command
					if deadline {
						name = "deadline-" + command
					}
					t.Run(name, func(t *testing.T) {
						ctx, rt := changesBackend(t, backend)
						f := changesFixture(t, ctx, rt, changestest.Options{AdvanceBase: true})
						before, err := changestest.Inventory(ctx, rt, f.Job)
						require.NoError(t, err)
						require.NoError(t, f.DeleteCheckout())
						require.NoError(t, os.RemoveAll(f.Origin))
						realGit, err := exec.LookPath("git")
						require.NoError(t, err)
						dir := t.TempDir()
						marker := filepath.Join(dir, "reached")
						wrapper := filepath.Join(dir, "git-targeted")
						script := "#!/bin/sh\nfor arg in \"$@\"; do\n if [ \"$arg\" = " + command + " ]; then\n  echo $$ > '" + marker + "'\n  sleep 30 &\n  echo $! > '" + marker + ".child'\n  wait\n fi\ndone\nexec '" + realGit + "' \"$@\"\n"
						require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
						temp := t.TempDir()
						readOnly := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
						gitTime := 5 * time.Second
						if deadline {
							// Allow ordinary Git startup under the full suite's load;
							// the targeted wrapper blocks for thirty seconds.
							gitTime = 2 * time.Second
						}
						api := changesAPI(t, readOnly, jobinspect.ChangesConfig{GitPath: wrapper, TempDir: temp, Limits: jobinspect.ChangesLimits{GitTime: gitTime}})
						readCtx, cancel := context.WithCancel(ctx)
						defer cancel()
						finished := make(chan error, 1)
						go func() {
							_, err := api.History(readCtx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
							finished <- err
						}()
						require.Eventually(t, func() bool { _, err := os.Stat(marker + ".child"); return err == nil }, 5*time.Second, 5*time.Millisecond, "targeted Git phase was never reached")
						want, code := context.Canceled, "canceled"
						if deadline {
							want, code = context.DeadlineExceeded, "deadline"
						} else {
							cancel()
						}
						select {
						case err := <-finished:
							require.ErrorIs(t, err, want)
							require.Equal(t, code, jobinspect.ChangesDiagnostic(err).Code)
						case <-time.After(5 * time.Second):
							t.Fatal("interrupted Git process did not release")
						}
						for _, path := range []string{marker, marker + ".child"} {
							pid, err := os.ReadFile(path)
							require.NoError(t, err)
							requireChangesProcessExit(t, pid)
						}
						files, err := os.ReadDir(temp)
						require.NoError(t, err)
						require.Empty(t, files)
						after, err := changestest.Inventory(ctx, rt, f.Job)
						require.NoError(t, err)
						require.Equal(t, string(before), string(after))
						require.Zero(t, readOnly.Mutations.Load())
					})
				}
			}
		})
	}
}

func TestChangesJ30L07RepositoryTransferCleanup(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{Commits: 1, OmitBaseBundle: true})
			require.NoError(t, f.DeleteCheckout())
			before, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			for _, mode := range []string{"limit", "deadline", "missing_trace"} {
				t.Run(mode, func(t *testing.T) {
					dir := t.TempDir()
					marker := filepath.Join(dir, "fetch")
					wrapper := filepath.Join(dir, "git-fetch")
					// Start a descendant before the real transfer, so a byte-limit
					// failure must stop the complete group and release the pipe.
					fetch := "echo $$ > '" + marker + "'\nsleep 30 &\necho $! > '" + marker + ".child'\n"
					if mode == "limit" {
						fetch += "'" + realGit + "' \"$@\"\nwait\n"
					} else if mode == "deadline" {
						fetch += "wait\n"
					} else {
						fetch = "unset GIT_TRACE_PACKFILE\n"
					}
					script := "#!/bin/sh\nfor arg in \"$@\"; do\nif [ \"$arg\" = fetch ]; then\n" + fetch + "fi\ndone\nexec '" + realGit + "' \"$@\"\n"
					require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
					tmp := t.TempDir()
					readOnly := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
					artifact := int64(len(f.Steps[len(f.Steps)-1].Bundle))
					limits := jobinspect.ChangesLimits{GitTime: 2 * time.Second}
					if mode == "limit" {
						limits.ArtifactBytes, limits.DownloadBytes = artifact, artifact+1
					}
					api := changesAPI(t, readOnly, jobinspect.ChangesConfig{GitPath: wrapper, TempDir: tmp, Limits: limits, ResolveRepository: func(context.Context, string) (jobinspect.RepositoryAccess, error) {
						return jobinspect.RepositoryAccess{URL: f.Origin}, nil
					}})
					_, err := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
					if mode == "deadline" {
						require.ErrorIs(t, err, context.DeadlineExceeded)
					} else if mode == "limit" {
						changesCode(t, err, "limit")
						require.Equal(t, "download_bytes", jobinspect.ChangesDiagnostic(err).Limit)
					} else {
						changesCode(t, err, "protocol")
					}
					if mode != "missing_trace" {
						for _, path := range []string{marker, marker + ".child"} {
							pid, err := os.ReadFile(path)
							require.NoError(t, err)
							requireChangesProcessExit(t, pid)
						}
					}
					files, err := os.ReadDir(tmp)
					require.NoError(t, err)
					require.Empty(t, files)
					after, err := changestest.Inventory(ctx, rt, f.Job)
					require.NoError(t, err)
					require.Equal(t, string(before), string(after))
					require.Zero(t, readOnly.Mutations.Load())
				})
			}
		})
	}
}
