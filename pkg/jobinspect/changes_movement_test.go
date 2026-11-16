package jobinspect_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type blockingChangesRuntime struct {
	jobdb.WorkflowRuntime
	gate func()
}

func (r *blockingChangesRuntime) OpenArtifact(ctx context.Context, q jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	if q.Name == gitstate.ThinPackArtifactName && r.gate != nil {
		r.gate()
	}
	return r.WorkflowRuntime.OpenArtifact(ctx, q)
}

func TestChangesJ21J36L05L06Movement(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			wrapped := &blockingChangesRuntime{WorkflowRuntime: rt}
			api := changesAPI(t, wrapped, jobinspect.ChangesConfig{})
			var saved *jobinspect.ChangesHistory
			var started, release chan struct{}
			var result chan *jobinspect.ChangesHistory
			var readError chan error
			opt := changestest.Options{JobID: "moving", Commits: 4, BeforeTask: func(i int) error {
				q := jobinspect.ChangesRequest{JobID: "moving", Limit: 1}
				if i == 3 {
					var e error
					saved, e = api.History(ctx, q)
					if e != nil {
						return e
					}
					require.Len(t, saved.Commits, 1)
					require.NotEmpty(t, saved.NextCursor)
					started = make(chan struct{})
					release = make(chan struct{})
					result = make(chan *jobinspect.ChangesHistory, 1)
					readError = make(chan error, 1)
					var once sync.Once
					wrapped.gate = func() { once.Do(func() { close(started); <-release }) }
					go func() { h, e := api.History(ctx, q); result <- h; readError <- e }()
					<-started
				}
				if i == 4 {
					close(release)
					old := <-result
					require.NoError(t, <-readError)
					require.Equal(t, saved.Head, old.Head)
					require.Equal(t, saved.Selection, old.Selection)
					wrapped.gate = nil
					q.Cursor = saved.NextCursor
					_, e := api.History(ctx, q)
					changesCode(t, e, "stale_selection")
					_, _, e = api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: "moving", Selection: saved.Selection}})
					changesCode(t, e, "stale_selection")
					q.Cursor = ""
					fresh, e := api.History(ctx, q)
					require.NoError(t, e)
					require.NotEqual(t, saved.Selection, fresh.Selection)
					require.NotEqual(t, saved.Head, fresh.Head)
				}
				return nil
			}}
			f := changesFixture(t, ctx, wrapped, opt)
			fresh, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, e)
			require.Equal(t, f.Head, fresh.Head)
		})
	}
}

func TestChangesJ12J13J15L01L02Isolation(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			t.Run("unrelated scope does not replace main", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
					if i == 3 {
						s.State.ScopeID = "foreign"
						s.Fail = true
					}
				}})
				api := changesAPI(t, rt, jobinspect.ChangesConfig{})
				h, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				require.NoError(t, e)
				require.Equal(t, f.Commits[1], h.Head)
			})
			t.Run("newer unavailable main blocks earlier success", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
					if i == 3 {
						s.State.HeadCommit = ""
						s.Fail = true
					}
				}})
				_, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				changesCode(t, e, "workspace_unavailable")
			})
			t.Run("missing root scope does not choose matching repository", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{RootContext: func(c *contextual.JobContext) { c.Workspace = nil }})
				_, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				changesCode(t, e, "workspace_unavailable")
			})
			t.Run("ambiguous explicit root", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{RootContext: func(c *contextual.JobContext) { c.Workspace.ScopeID = "" }})
				_, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				changesCode(t, e, "workspace_ambiguous")
			})
			t.Run("unpersisted head rejects source", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
					if i == 3 {
						s.State.HeadCommit = strings.Repeat("a", 40)
					}
				}})
				_, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				changesCode(t, e, "source_mismatch")
			})
			t.Run("live dirty files are outside recorded content", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{})
				api := changesAPI(t, rt, jobinspect.ChangesConfig{})
				before, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				require.NoError(t, e)
				_, e = changestest.Git(ctx, f.Checkout, "commit", "--allow-empty", "-m", "unrecorded")
				require.NoError(t, e)
				require.NoError(t, os.WriteFile(filepath.Join(f.Checkout, "note.txt"), []byte("dirty uncommitted\n"), 0600))
				after, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				require.NoError(t, e)
				require.Equal(t, before, after)
			})
		})
	}
}

func TestChangesJ28MalformedSources(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			for _, variant := range []string{"header", "checksum", "truncated", "tree"} {
				t.Run(variant, func(t *testing.T) {
					f := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
						if i != 3 {
							return
						}
						switch variant {
						case "header":
							s.Bundle = []byte("not a bundle\n")
						case "checksum":
							s.Bundle = append([]byte(nil), s.Bundle...)
							s.Bundle[len(s.Bundle)-1] ^= 255
						case "truncated":
							s.Bundle = s.Bundle[:len(s.Bundle)/2]
						case "tree":
							var err error
							s.Bundle, err = changestest.BreakTree(s.Bundle)
							require.NoError(t, err)
						}
					}})
					_, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
					if variant == "tree" {
						require.Error(t, e)
						require.Contains(t, []string{"invalid_objects", "missing_closure"}, jobinspect.ChangesDiagnostic(e).Code)
					} else {
						changesCode(t, e, "invalid_objects")
					}
				})
			}
		})
	}
}

func TestChangesJ32EarlyCloseAndCancellation(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{})
			tmp := t.TempDir()
			api := changesAPI(t, rt, jobinspect.ChangesConfig{TempDir: tmp})
			cancelCtx, cancel := context.WithCancel(ctx)
			_, body, e := api.OpenDiff(cancelCtx, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
			require.NoError(t, e)
			_, e = body.Read(make([]byte, 1))
			require.NoError(t, e)
			require.NoError(t, body.Close())
			require.NoError(t, body.Close())
			cancel()
			_, e = io.ReadAll(body)
			require.ErrorIs(t, e, context.Canceled)
			abandoned, stop := context.WithCancel(ctx)
			defer stop()
			_, body, e = api.OpenDiff(abandoned, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
			require.NoError(t, e)
			stop()
			require.Eventually(t, func() bool { files, e := os.ReadDir(tmp); return e == nil && len(files) == 0 }, time.Second, 10*time.Millisecond)
			require.NoError(t, body.Close())
		})
	}
}
