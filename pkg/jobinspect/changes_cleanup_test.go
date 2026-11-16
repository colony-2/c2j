//go:build unix

package jobinspect_test

import (
	"context"
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
	jobdb.WorkflowRuntime
	started chan struct{}
	closed  chan struct{}
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
	a, e := r.WorkflowRuntime.OpenArtifact(ctx, q)
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
			wrapped := &arrivingChangesRuntime{WorkflowRuntime: rt, started: make(chan struct{}), closed: make(chan struct{})}
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
				state, _ := exec.Command("ps", "-o", "stat=", "-p", strings.TrimSpace(string(pid))).Output()
				require.True(t, len(strings.TrimSpace(string(state))) == 0 || strings.HasPrefix(strings.TrimSpace(string(state)), "Z"), "process %s still running: %s", pid, state)
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
						readOnly := &changestest.ReadOnlyRuntime{WorkflowRuntime: rt}
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
							state, _ := exec.Command("ps", "-o", "stat=", "-p", strings.TrimSpace(string(pid))).Output()
							require.True(t, len(strings.TrimSpace(string(state))) == 0 || strings.HasPrefix(strings.TrimSpace(string(state)), "Z"), "process %s still running: %s", pid, state)
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
