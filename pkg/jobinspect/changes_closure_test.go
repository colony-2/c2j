package jobinspect_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/stretchr/testify/require"
)

func TestChangesJ04J05UpstreamAndFastForward(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{FastForward: true})
			calls := 0
			api := changesAPI(t, rt, jobinspect.ChangesConfig{ResolveRepository: func(context.Context, string) (jobinspect.RepositoryAccess, error) {
				calls++
				return jobinspect.RepositoryAccess{URL: f.Origin}, nil
			}})
			h, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, e)
			require.Len(t, h.Commits, 3)
			for i, c := range h.Commits {
				require.Equal(t, f.Commits[i], c.Hash)
				require.Len(t, c.Parents, 1)
				if i > 0 {
					require.Less(t, c.Author.Time, h.Commits[i-1].Author.Time)
				}
			}
			branch, e := changestest.Git(ctx, f.Origin, "symbolic-ref", "HEAD")
			require.NoError(t, e)
			_, e = changestest.Git(ctx, f.Checkout, "push", f.Origin, f.Head+":"+strings.TrimSpace(string(branch)))
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			fresh, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, e)
			require.Equal(t, h, fresh)
			require.Zero(t, calls, "retained closure must be resolved before configured repository access")
		})
	}
}

func TestChangesJ24NoPackEqualEndpointsMissingTrees(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{Published: true, Transform: func(_ int, s *changestest.Step) { s.Bundle = nil }})
			require.NoError(t, f.DeleteCheckout())
			require.NoError(t, os.RemoveAll(f.Origin))
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId}
			api := changesAPI(t, rt, jobinspect.ChangesConfig{})
			_, e := api.History(ctx, q)
			changesCode(t, e, "missing_closure")
			require.Equal(t, []string{f.Head}, jobinspect.ChangesDiagnostic(e).Missing)
			_, _, e = api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: q})
			changesCode(t, e, "missing_closure")
		})
	}
}

func TestChangesJ25GitHTTPAccessFailures(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{OmitBaseBundle: true})
			require.NoError(t, f.DeleteCheckout())
			for _, status := range []int{401, 403, 500} {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					require.Empty(t, r.Header.Get("Authorization"))
					w.WriteHeader(status)
				}))
				api := changesAPI(t, rt, jobinspect.ChangesConfig{ResolveRepository: func(context.Context, string) (jobinspect.RepositoryAccess, error) {
					return jobinspect.RepositoryAccess{URL: server.URL}, nil
				}})
				_, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				changesCode(t, e, map[int]string{401: "repository_authentication", 403: "repository_denied", 500: "repository_unavailable"}[status])
				server.Close()
			}
		})
	}
}

func TestChangesJ26OffChainBase(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			for _, merge := range []int{0, 1} {
				f := changesFixture(t, ctx, rt, changestest.Options{MergeAt: merge})
				_, e := changestest.Git(ctx, f.Checkout, "checkout", "--orphan", "unrelated")
				require.NoError(t, e)
				_, e = changestest.Git(ctx, f.Checkout, "commit", "--allow-empty", "-m", "off chain")
				require.NoError(t, e)
				raw, e := changestest.Git(ctx, f.Checkout, "rev-parse", "HEAD")
				require.NoError(t, e)
				hash := strings.TrimSpace(string(raw))
				_, e = changestest.Git(ctx, f.Checkout, "push", f.Origin, hash+":refs/heads/unrelated")
				require.NoError(t, e)
				require.NoError(t, f.DeleteCheckout())
				api := changesAPI(t, rt, jobinspect.ChangesConfig{ResolveRepository: func(context.Context, string) (jobinspect.RepositoryAccess, error) {
					return jobinspect.RepositoryAccess{URL: f.Origin}, nil
				}})
				q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Base: hash}
				_, e = api.History(ctx, q)
				changesCode(t, e, "invalid_range")
				_, _, e = api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: q})
				changesCode(t, e, "invalid_range")
			}
		})
	}
}

func TestChangesJ03ActiveBoundaryPrecedence(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			var actualBase string
			f := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
				if i == 0 {
					actualBase = s.State.HeadCommit
				}
				if i > 0 {
					s.Git = &contextual.GitCommitContext{ParentHash: actualBase, PersistHash: s.State.HeadCommit}
					s.State.BaseCommit = strings.Repeat("f", 40)
				}
			}, InputContext: func(i int, g *gitstate.GlobalGitTaskContext) {
				if i > 0 {
					g.ResolvedBaseHash = strings.Repeat("e", 40)
				}
			}})
			h, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, e)
			require.Equal(t, actualBase, h.ActiveBase)
			require.Equal(t, actualBase, h.Base)
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Base: f.Commits[0]}
			h, e = changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, q)
			require.NoError(t, e)
			require.Equal(t, actualBase, h.ActiveBase)
			require.Equal(t, "output.parent_hash", h.ActiveBaseOrigin)
			require.Equal(t, q.Base, h.Base)
			require.Equal(t, "explicit", h.BaseOrigin)
		})
	}
}

func TestChangesJ10J26ShallowWriterBundle(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			dir := t.TempDir()
			f, e := changestest.Build(ctx, rt, changestest.Options{Directory: dir, Transform: func(i int, s *changestest.Step) {
				s.Bundle = nil
				if i != 3 {
					return
				}
				shallow := filepath.Join(dir, "shallow")
				_, e := changestest.Git(ctx, dir, "clone", "--quiet", "--depth", "1", "file://"+filepath.Join(dir, "checkout"), shallow)
				require.NoError(t, e)
				out, e := gitcommit.PersistCommit(ctx, gitcommit.PersistCommitActivity{RepoPath: shallow, StorageLocation: filepath.Join(dir, "shallow-packs"), RootHash: s.State.HeadCommit, ExpectedHeadHash: s.State.BaseCommit})
				require.NoError(t, e)
				s.Bundle, e = os.ReadFile(out.ThinPackPath)
				require.NoError(t, e)
			}})
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			_, e = changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.Error(t, e)
			diagnostic := jobinspect.ChangesDiagnostic(e)
			require.Contains(t, []string{"missing_closure", "invalid_objects"}, diagnostic.Code)
		})
	}
}

func TestChangesU06DuplicateAndCyclicPrerequisites(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			for _, cyclic := range []bool{false, true} {
				f := changesFixture(t, ctx, rt, changestest.Options{Transform: func(i int, s *changestest.Step) {
					if i != 3 {
						return
					}
					header, body, ok := strings.Cut(string(s.Bundle), "\n\n")
					require.True(t, ok)
					if cyclic {
						header += "\n-" + s.State.HeadCommit + " self"
					} else {
						header += "\n-" + s.State.BaseCommit + " duplicate"
					}
					s.Bundle = []byte(header + "\n\n" + body)
				}})
				h, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
				if cyclic {
					changesCode(t, e, "invalid_objects")
				} else {
					require.NoError(t, e)
					require.Len(t, h.Sources, 2)
				}
			}
		})
	}
}

func TestChangesJ20J21EarlierCumulativeCoverageSkipsUnrelated(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			dir := t.TempDir()
			var first, head string
			f, e := changestest.Build(ctx, rt, changestest.Options{Directory: dir, Commits: 4, Transform: func(i int, s *changestest.Step) {
				checkout := filepath.Join(dir, "checkout")
				if i == 1 {
					first = s.State.HeadCommit
					s.Bundle = nil
				}
				if i == 3 {
					raw, err := changestest.Git(ctx, checkout, "rev-parse", "HEAD")
					require.NoError(t, err)
					head = strings.TrimSpace(string(raw))
					_, err = changestest.Git(ctx, checkout, "checkout", "--orphan", "unrelated-earlier")
					require.NoError(t, err)
					out, err := gitcommit.PersistCommit(ctx, gitcommit.PersistCommitActivity{RepoPath: checkout, StorageLocation: filepath.Join(dir, "unrelated-packs"), EmptyBase: true, CommitMessage: "unrelated recorded branch"})
					require.NoError(t, err)
					s.State.BaseCommit = ""
					s.State.EmptyBase = true
					s.State.HeadCommit = out.CommitHash
					s.Bundle, err = os.ReadFile(out.ThinPackPath)
					require.NoError(t, err)
					_, err = changestest.Git(ctx, checkout, "checkout", "--detach", head)
					require.NoError(t, err)
				}
				if i == 4 {
					out, err := gitcommit.PersistCommit(ctx, gitcommit.PersistCommitActivity{RepoPath: checkout, StorageLocation: filepath.Join(dir, "latest-packs"), RootHash: first, ExpectedHeadHash: first})
					require.NoError(t, err)
					s.State.BaseCommit = first
					s.State.HeadCommit = out.CommitHash
					s.Bundle, err = os.ReadFile(out.ThinPackPath)
					require.NoError(t, err)
				}
			}})
			require.NoError(t, e)
			oracle, e := f.Oracle(ctx, first, f.Head)
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			require.NoError(t, os.RemoveAll(f.Origin))
			before, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			log, e := (&jobinspect.Service{Runtime: rt, Tenant: "tenant"}).Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, e)
			tmp := t.TempDir()
			api := changesAPI(t, rt, jobinspect.ChangesConfig{TempDir: tmp})
			h, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, e)
			require.Len(t, h.Commits, 3)
			require.Len(t, h.Sources, 3)
			for _, s := range h.Sources {
				require.NotEqual(t, log.Tasks[3].ID, s.Producer.Task, "unrelated probe must not enter source identity")
			}
			_, patch := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
			require.Equal(t, oracle, patch)
			after, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
			files, e := os.ReadDir(tmp)
			require.NoError(t, e)
			require.Empty(t, files)
		})
	}
}
