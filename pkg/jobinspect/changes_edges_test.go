package jobinspect_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestChangesJ08J09J10J26States(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			t.Run("published base equals head still proves trees", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{Published: true})
				require.NoError(t, f.DeleteCheckout())
				api := changesAPI(t, rt, jobinspect.ChangesConfig{})
				h, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				require.NoError(t, e)
				require.Empty(t, h.Commits)
				require.NotEmpty(t, h.HeadTree)
				require.Equal(t, h.Head, h.Base)
				require.Equal(t, h.HeadTree, h.BaseTree)
				_, patch := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
				require.Empty(t, patch)
			})
			t.Run("unborn and unknown remain distinct", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
					if i == 3 {
						s.State.EmptyBase = true
						s.State.BaseCommit = ""
						s.State.HeadCommit = ""
						s.Bundle = nil
					}
				}})
				api := changesAPI(t, rt, jobinspect.ChangesConfig{})
				h, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				require.NoError(t, e)
				require.True(t, h.Unborn)
				require.True(t, h.EmptyBase)
				require.True(t, h.Complete)
				require.Empty(t, h.Commits)
				require.Equal(t, "unavailable", h.Diff)
				_, _, e = api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
				changesCode(t, e, "workspace_unavailable")
				unknown := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
					if i == 3 {
						s.State.BaseCommit = ""
						s.State.EmptyBase = false
					}
				}})
				_, e = api.History(ctx, jobinspect.ChangesRequest{JobID: unknown.Job.JobId})
				changesCode(t, e, "workspace_unavailable")
			})
			t.Run("detached rewritten chain", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{Detached: true, Rebase: true})
				raw, e := changestest.Git(ctx, f.Checkout, "rev-list", "--reverse", f.Base+".."+f.Head)
				require.NoError(t, e)
				expected := strings.Fields(string(raw))
				require.NotEqual(t, f.Commits[1], expected[1])
				require.NoError(t, f.DeleteCheckout())
				h, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				require.NoError(t, e)
				for i, c := range h.Commits {
					require.Equal(t, expected[i], c.Hash)
				}
			})
			t.Run("writer fallback retains complete ancestry", func(t *testing.T) {
				f := changesFixture(t, ctx, rt, changestest.Options{Fallback: true})
				oracle, e := f.Oracle(ctx, f.Base, f.Head)
				require.NoError(t, e)
				require.NoError(t, f.DeleteCheckout())
				api := changesAPI(t, rt, jobinspect.ChangesConfig{})
				q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Base: f.Base}
				h, e := api.History(ctx, q)
				require.NoError(t, e)
				require.Len(t, h.Commits, 3)
				require.Len(t, h.Sources, 4)
				_, b := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: q})
				require.Equal(t, oracle, b)
			})
		})
	}
}

func TestChangesJ30AllBudgets(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{AdvanceBase: true})
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Base: f.Base}
			var largest int64
			for _, s := range f.Steps {
				largest = max(largest, int64(len(s.Bundle)))
			}
			for _, tc := range []struct {
				name   string
				limits jobinspect.ChangesLimits
				modify func(*jobinspect.ChangesRequest)
			}{
				{"cursor_bytes", jobinspect.ChangesLimits{CursorBytes: 1}, func(q *jobinspect.ChangesRequest) { q.Cursor = "long" }},
				{"header_bytes", jobinspect.ChangesLimits{HeaderBytes: 1}, nil},
				{"artifact_bytes", jobinspect.ChangesLimits{ArtifactBytes: 1}, nil},
				{"download_bytes", jobinspect.ChangesLimits{ArtifactBytes: largest, DownloadBytes: largest + 1}, nil},
				{"lineage_candidates", jobinspect.ChangesLimits{LineageCandidates: 1}, nil},
				{"prerequisite_sources", jobinspect.ChangesLimits{PrerequisiteSources: 1}, nil},
				{"depth", jobinspect.ChangesLimits{Depth: 1}, nil},
				{"commits", jobinspect.ChangesLimits{Commits: 1}, nil},
				{"objects", jobinspect.ChangesLimits{Objects: 1}, nil},
				{"object_bytes", jobinspect.ChangesLimits{ObjectBytes: 1}, nil},
				{"inflated_bytes", jobinspect.ChangesLimits{ObjectBytes: 512, InflatedBytes: 512}, nil},
				{"temporary_bytes", jobinspect.ChangesLimits{TemporaryBytes: 1, PatchBytes: 1}, nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					tmp := t.TempDir()
					api := changesAPI(t, rt, jobinspect.ChangesConfig{TempDir: tmp, Limits: tc.limits})
					request := q
					if tc.modify != nil {
						tc.modify(&request)
					}
					_, e := api.History(ctx, request)
					changesCode(t, e, "limit")
					require.Equal(t, tc.name, jobinspect.ChangesDiagnostic(e).Limit)
					entries, e := os.ReadDir(tmp)
					require.NoError(t, e)
					require.Empty(t, entries)
				})
			}
			for _, limits := range []jobinspect.ChangesLimits{{OperationTime: time.Nanosecond}, {GitTime: time.Nanosecond}} {
				api := changesAPI(t, rt, jobinspect.ChangesConfig{Limits: limits})
				_, e := api.History(ctx, q)
				require.ErrorIs(t, e, context.DeadlineExceeded)
			}
		})
	}
}

func TestChangesJ33AmbientIsolation(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			dir := t.TempDir()
			var recordedHead string
			f, e := changestest.Build(ctx, rt, changestest.Options{Directory: dir, Transform: func(i int, s *changestest.Step) {
				if i != 3 {
					return
				}
				checkout := filepath.Join(dir, "checkout")
				require.NoError(t, os.WriteFile(filepath.Join(checkout, ".gitattributes"), []byte("*.dat diff=evil filter=lfs\n"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(checkout, ".gitmodules"), []byte("[submodule \"opaque\"]\n path = opaque\n url = ext::forbidden-transport\n"), 0600))
				require.NoError(t, os.Symlink("/unreadable-target", filepath.Join(checkout, "opaque-link")))
				_, err := changestest.Git(ctx, checkout, "update-index", "--add", "--cacheinfo", "160000,"+s.State.BaseCommit+",opaque")
				require.NoError(t, err)
				out, err := gitcommit.PersistCommit(ctx, gitcommit.PersistCommitActivity{RepoPath: checkout, StorageLocation: filepath.Join(dir, "isolation-packs"), RootHash: s.State.BaseCommit, ExpectedHeadHash: s.State.HeadCommit, CommitMessage: "opaque submodule, attributes and symlink"})
				require.NoError(t, err)
				s.Bundle, err = os.ReadFile(out.ThinPackPath)
				require.NoError(t, err)
				s.State.HeadCommit = out.CommitHash
				recordedHead = out.CommitHash
			}})
			require.NoError(t, e)
			before, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			oracle, e := f.Oracle(ctx, f.Base, recordedHead)
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			marker := filepath.Join(t.TempDir(), "executed")
			config := filepath.Join(t.TempDir(), "evil.gitconfig")
			require.NoError(t, os.WriteFile(config, []byte("[diff]\n external = touch "+marker+"\n[diff \"evil\"]\n textconv = touch "+marker+"\n[filter \"lfs\"]\n smudge = touch "+marker+"\n[core]\n hooksPath = /does/not/exist\n[protocol \"file\"]\n allow = always\n"), 0600))
			for _, kv := range [][2]string{{"GIT_CONFIG_GLOBAL", config}, {"GIT_CONFIG_SYSTEM", config}, {"GIT_CONFIG_COUNT", "1"}, {"GIT_CONFIG_KEY_0", "diff.external"}, {"GIT_CONFIG_VALUE_0", "touch " + marker}, {"GIT_ALTERNATE_OBJECT_DIRECTORIES", f.Origin}, {"GIT_OBJECT_DIRECTORY", f.Origin}, {"GIT_DIR", f.Origin}, {"GIT_REPLACE_REF_BASE", "refs/evil/"}, {"GIT_CONFIG_PARAMETERS", "'diff.external=touch " + marker + "'"}, {"GIT_SSH_COMMAND", "touch " + marker}} {
				t.Setenv(kv[0], kv[1])
			}
			api := changesAPI(t, rt, jobinspect.ChangesConfig{})
			_, b := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
			require.Equal(t, oracle, b)
			_, e = os.Stat(marker)
			require.True(t, os.IsNotExist(e))
			after, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestChangesJ41AuthorizationBeforeBytes(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{})
			api, e := jobinspect.NewChangesService(&jobinspect.Service{Runtime: rt, Tenant: "tenant", Authorize: func(context.Context, string) error { return jobdb.ErrCellForbidden }}, jobinspect.ChangesConfig{})
			require.NoError(t, e)
			_, e = api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			changesCode(t, e, "job_denied")
			wrong, e := jobinspect.NewChangesService(&jobinspect.Service{Runtime: rt, Tenant: "other"}, jobinspect.ChangesConfig{})
			require.NoError(t, e)
			_, e = wrong.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.Error(t, e)
		})
	}
}

// The current recorded input is authoritative while the next native task is
// running. No dirty checkout inspection or before-snapshot approximation occurs.
func TestChangesL03RecordedIncoming(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			var previous changestest.Step
			f := changesFixture(t, ctx, rt, changestest.Options{WaitAt: 3, Transform: func(i int, s *changestest.Step) {
				if i == 2 {
					previous = *s
				}
				if i == 3 {
					s.State = previous.State
					s.Bundle = nil
				}
			}})
			h, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, e)
			require.Equal(t, "before", h.Boundary)
			require.Equal(t, previous.State.HeadCommit, h.Head)
			require.Len(t, h.Commits, 2)
		})
	}
}
