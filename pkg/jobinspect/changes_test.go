package jobinspect_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func changesBackend(t *testing.T, backend string) (context.Context, jobdb.ConsumerRuntime) {
	t.Helper()
	ctx := celltest.Context(context.Background())
	rt, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "changes.db")})
	require.NoError(t, e)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	var runtime jobdb.ConsumerRuntime = rt
	if backend == "http" {
		server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
		t.Cleanup(server.Close)
		client, e := remote.New(server.URL, server.Client())
		require.NoError(t, e)
		runtime = client
	}
	celltest.Create(t, runtime, "tenant", "changes", "example.test/changes")
	return ctx, runtime
}

func changesFixture(t *testing.T, ctx context.Context, rt jobdb.ConsumerRuntime, opt changestest.Options) *changestest.Fixture {
	t.Helper()
	opt.Directory = t.TempDir()
	f, e := changestest.Build(ctx, rt, opt)
	require.NoError(t, e)
	before, e := changestest.Inventory(ctx, rt, f.Job)
	require.NoError(t, e)
	t.Cleanup(func() {
		after, e := changestest.Inventory(ctx, rt, f.Job)
		require.NoError(t, e)
		require.Equal(t, string(before), string(after), "reads must preserve the complete durable fixture")
	})
	return f
}
func changesAPI(t *testing.T, rt jobdb.ConsumerRuntime, config jobinspect.ChangesConfig) *jobinspect.ChangesService {
	t.Helper()
	guard := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
	t.Cleanup(func() { require.Zero(t, guard.Mutations.Load()) })
	s, e := jobinspect.NewChangesService(&jobinspect.Service{Runtime: guard, Tenant: "tenant"}, config)
	require.NoError(t, e)
	return s
}
func changesCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	var e *jobinspect.ChangesError
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code, err)
}
func readPatch(t *testing.T, ctx context.Context, api jobinspect.ChangesAPI, q jobinspect.DiffRequest) (*jobinspect.ChangesDiff, []byte) {
	t.Helper()
	m, r, e := api.OpenDiff(ctx, q)
	require.NoError(t, e)
	defer r.Close()
	b, e := io.ReadAll(r)
	require.NoError(t, e)
	require.NoError(t, r.Close())
	require.Equal(t, int64(len(b)), m.Bytes)
	return m, b
}

func TestChangesJ16J17J20J23J29J31J32J43(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{Unchanged: true})
			oracle, e := f.Oracle(ctx, f.Base, f.Head)
			require.NoError(t, e)
			singles := map[string][]byte{}
			trees := map[string]string{}
			for _, hash := range append([]string{f.Base}, f.Commits...) {
				raw, e := changestest.Git(ctx, f.Checkout, "rev-parse", hash+"^{tree}")
				require.NoError(t, e)
				trees[hash] = strings.TrimSpace(string(raw))
			}
			prev := f.Base
			for _, hash := range f.Commits {
				b, e := f.Oracle(ctx, prev, hash)
				require.NoError(t, e)
				singles[hash] = b
				prev = hash
			}
			rangeOracle, e := f.Oracle(ctx, f.Base, f.Commits[1])
			require.NoError(t, e)
			interiorOracle, e := f.Oracle(ctx, f.Commits[0], f.Head)
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			before, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			tmp := t.TempDir()
			readOnly := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
			api := changesAPI(t, readOnly, jobinspect.ChangesConfig{TempDir: tmp})
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Limit: 1}
			var rows []jobinspect.ChangesCommit
			var selection string
			var stateTask string
			for {
				p, e := api.History(ctx, q)
				require.NoError(t, e)
				require.True(t, p.Complete)
				require.Equal(t, 3, p.Total)
				require.Len(t, p.Sources, 2)
				require.Equal(t, f.Base, p.Base)
				require.Equal(t, f.Head, p.Head)
				require.Equal(t, "sha1", p.ObjectFormat)
				rows = append(rows, p.Commits...)
				if selection == "" {
					selection = p.Selection
					stateTask = p.State.Task
				}
				require.Equal(t, selection, p.Selection)
				require.NotEqual(t, stateTask, p.Sources[0].Producer.Task)
				if p.NextCursor == "" {
					break
				}
				q.Cursor = p.NextCursor
			}
			for i, r := range rows {
				require.Equal(t, f.Commits[i], r.Hash)
				require.Equal(t, i+1, r.Position)
				require.Contains(t, r.Subject, "| μ")
				require.Contains(t, r.Author.Name, "μ")
				m, b := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId, Selection: selection}, Commit: r.Hash})
				require.Equal(t, singles[r.Hash], b)
				require.Equal(t, r.Hash, m.ToCommit)
				predecessor := f.Base
				if i > 0 {
					predecessor = rows[i-1].Hash
				}
				require.Equal(t, predecessor, m.FromCommit)
				require.Equal(t, trees[predecessor], m.FromTree)
				require.Equal(t, trees[r.Hash], m.ToTree)
			}
			m, b := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId, Selection: selection}})
			require.Equal(t, oracle, b)
			require.Equal(t, f.Base, m.FromCommit)
			require.Equal(t, f.Head, m.ToCommit)
			require.Equal(t, trees[f.Base], m.FromTree)
			require.Equal(t, trees[f.Head], m.ToTree)
			m, b = readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}, From: f.Commits[0], Through: f.Commits[1]})
			require.Equal(t, rangeOracle, b)
			require.Equal(t, f.Base, m.FromCommit)
			require.Equal(t, f.Commits[1], m.ToCommit)
			m, b = readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}, From: f.Commits[1], Through: f.Head})
			require.Equal(t, interiorOracle, b)
			require.Equal(t, f.Commits[0], m.FromCommit)
			require.Equal(t, trees[f.Commits[0]], m.FromTree)
			require.Equal(t, f.Head, m.ToCommit)
			require.Equal(t, trees[f.Head], m.ToTree)
			_, b = readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}, From: f.Commits[0], Through: f.Head})
			require.Equal(t, oracle, b)
			_, _, e = api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}, From: f.Commits[2], Through: f.Commits[0]})
			changesCode(t, e, "invalid_range")
			after, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
			files, e := os.ReadDir(tmp)
			require.NoError(t, e)
			require.Empty(t, files)
			require.Zero(t, readOnly.Mutations.Load())
		})
	}
}

func TestChangesJ18Revert(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{Revert: true})
			require.NoError(t, f.DeleteCheckout())
			api := changesAPI(t, rt, jobinspect.ChangesConfig{})
			h, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, e)
			require.Len(t, h.Commits, 3)
			m, b := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
			require.Empty(t, b)
			require.True(t, m.Complete)
			require.Equal(t, m.FromTree, m.ToTree)
		})
	}
}

func TestChangesJ06J07J19Merges(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			for _, at := range []int{1, 3, -1} {
				t.Run(string(rune(at+65)), func(t *testing.T) {
					f := changesFixture(t, ctx, rt, changestest.Options{MergeAt: at})
					oracle, e := f.Oracle(ctx, f.Base, f.Head)
					require.NoError(t, e)
					require.NoError(t, f.DeleteCheckout())
					api := changesAPI(t, rt, jobinspect.ChangesConfig{})
					q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Limit: 1}
					h, e := api.History(ctx, q)
					require.NoError(t, e)
					m, b := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}})
					require.Equal(t, oracle, b)
					if at == -1 {
						require.True(t, h.Complete)
						require.Equal(t, "complete", m.History)
					} else {
						require.False(t, h.Complete)
						require.Empty(t, h.Commits)
						require.Empty(t, h.NextCursor)
						require.Equal(t, "unsupported", m.History)
						for _, hash := range f.Commits {
							_, _, e = api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId}, Commit: hash})
							changesCode(t, e, "unsupported_history")
						}
					}
				})
			}
		})
	}
}

func TestChangesJ22J24J25Access(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{OmitBaseBundle: true})
			require.NoError(t, f.DeleteCheckout())
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId}
			_, e := changesAPI(t, rt, jobinspect.ChangesConfig{}).History(ctx, q)
			changesCode(t, e, "missing_closure")
			var ce *jobinspect.ChangesError
			require.ErrorAs(t, e, &ce)
			require.Equal(t, []string{f.Base}, ce.Missing)
			calls := 0
			api := changesAPI(t, rt, jobinspect.ChangesConfig{ResolveRepository: func(_ context.Context, repo string) (jobinspect.RepositoryAccess, error) {
				calls++
				require.Equal(t, "example.test/changes", repo)
				return jobinspect.RepositoryAccess{URL: f.Origin}, nil
			}})
			h, e := api.History(ctx, q)
			require.NoError(t, e)
			require.Equal(t, 1, calls)
			require.Equal(t, []string{f.Base}, h.RepositoryObjects)
			for _, code := range []string{"repository_denied", "repository_authentication"} {
				api := changesAPI(t, rt, jobinspect.ChangesConfig{ResolveRepository: func(context.Context, string) (jobinspect.RepositoryAccess, error) {
					return jobinspect.RepositoryAccess{}, &jobinspect.ChangesError{Version: 1, Code: code, Detail: "fixture denial"}
				}})
				_, e = api.History(ctx, q)
				changesCode(t, e, code)
			}
		})
	}
}

func TestChangesJ01J02J03J04J27Boundaries(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			for _, format := range []string{"sha1", "sha256"} {
				t.Run(format, func(t *testing.T) {
					f := changesFixture(t, ctx, rt, changestest.Options{Format: format, EmptyBase: true})
					oracle, e := f.Oracle(ctx, f.Commits[0], f.Head)
					require.NoError(t, e)
					empty, e := changestest.Git(ctx, f.Checkout, "mktree")
					require.NoError(t, e)
					emptyTree := strings.TrimSpace(string(empty))
					rootOracle, e := f.Oracle(ctx, emptyTree, f.Base)
					require.NoError(t, e)
					allOracle, e := f.Oracle(ctx, emptyTree, f.Head)
					require.NoError(t, e)
					require.NoError(t, f.DeleteCheckout())
					api := changesAPI(t, rt, jobinspect.ChangesConfig{})
					q := jobinspect.ChangesRequest{JobID: f.Job.JobId}
					h, e := api.History(ctx, q)
					require.NoError(t, e)
					require.True(t, h.EmptyBase)
					require.Equal(t, format, h.ObjectFormat)
					require.Len(t, h.Commits, 4)
					require.Equal(t, emptyTree, h.BaseTree)
					root, patch := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: q, Commit: f.Base})
					require.Equal(t, emptyTree, root.FromTree)
					require.Empty(t, root.FromCommit)
					require.Equal(t, rootOracle, patch)
					_, patch = readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: q})
					require.Equal(t, allOracle, patch)
					q.Base = f.Commits[0]
					h, e = api.History(ctx, q)
					require.NoError(t, e)
					require.Equal(t, "explicit", h.BaseOrigin)
					require.Empty(t, h.ActiveBase)
					m, b := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: q})
					require.Equal(t, oracle, b)
					require.Equal(t, f.Commits[0], m.FromCommit)
					q.Base = strings.Repeat("f", len(f.Base))
					_, e = api.History(ctx, q)
					changesCode(t, e, "missing_closure")
				})
			}
		})
	}
}

func TestChangesU03J29Cursors(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{})
			api := changesAPI(t, rt, jobinspect.ChangesConfig{})
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Limit: 1}
			h, e := api.History(ctx, q)
			require.NoError(t, e)
			q.Cursor = h.NextCursor
			for _, mutate := range []func(map[string]any){func(v map[string]any) { v["Tenant"] = "other" }, func(v map[string]any) { v["JobID"] = "other" }, func(v map[string]any) { v["Base"] = "other" }, func(v map[string]any) { v["Limit"] = 2 }, func(v map[string]any) { v["Next"] = -1 }, func(v map[string]any) { v["Next"] = 9 }} {
				raw, e := base64.RawURLEncoding.DecodeString(h.NextCursor)
				require.NoError(t, e)
				var v map[string]any
				require.NoError(t, json.Unmarshal(raw, &v))
				mutate(v)
				raw, e = json.Marshal(v)
				require.NoError(t, e)
				q.Cursor = base64.RawURLEncoding.EncodeToString(raw)
				_, e = api.History(ctx, q)
				changesCode(t, e, "invalid_cursor")
			}
			q.Cursor = h.NextCursor
			raw, e := base64.RawURLEncoding.DecodeString(h.NextCursor)
			require.NoError(t, e)
			var unsupported map[string]any
			require.NoError(t, json.Unmarshal(raw, &unsupported))
			unsupported["Version"] = 999
			raw, e = json.Marshal(unsupported)
			require.NoError(t, e)
			q.Cursor = base64.RawURLEncoding.EncodeToString(raw)
			_, e = api.History(ctx, q)
			changesCode(t, e, "protocol")
			q.Cursor = h.NextCursor
			q.Selection = "different"
			_, e = api.History(ctx, q)
			changesCode(t, e, "invalid_cursor")
		})
	}
}

func TestChangesU01U04U05J30J34(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{})
			q := jobinspect.ChangesRequest{JobID: f.Job.JobId}
			for _, config := range []jobinspect.ChangesConfig{{Limits: jobinspect.ChangesLimits{Commits: 1}}, {Limits: jobinspect.ChangesLimits{ArtifactBytes: 1}}, {Limits: jobinspect.ChangesLimits{Objects: 1}}, {Limits: jobinspect.ChangesLimits{ObjectBytes: 1}}, {Limits: jobinspect.ChangesLimits{HeaderBytes: 1}}} {
				api := changesAPI(t, rt, config)
				_, e := api.History(ctx, q)
				changesCode(t, e, "limit")
			}
			_, e := jobinspect.NewChangesService(&jobinspect.Service{Runtime: rt, Tenant: "tenant"}, jobinspect.ChangesConfig{Limits: jobinspect.ChangesLimits{Depth: -1}})
			changesCode(t, e, "invalid_request")
			api := changesAPI(t, rt, jobinspect.ChangesConfig{GitPath: "/missing/git"})
			_, e = api.History(ctx, q)
			changesCode(t, e, "git_unavailable")
			api = changesAPI(t, rt, jobinspect.ChangesConfig{TempDir: "/missing/directory"})
			_, e = api.History(ctx, q)
			changesCode(t, e, "temporary_storage")
			api = changesAPI(t, rt, jobinspect.ChangesConfig{})
			cancelCtx, cancel := context.WithCancel(ctx)
			cancel()
			_, e = api.History(cancelCtx, q)
			require.ErrorIs(t, e, context.Canceled)
			for _, d := range []jobinspect.DiffRequest{{All: true, Commit: f.Head}, {From: f.Head}, {Commit: "abc"}} {
				d.JobID = f.Job.JobId
				_, _, e = api.OpenDiff(ctx, d)
				changesCode(t, e, "invalid_range")
			}
			_, _, e = changesAPI(t, rt, jobinspect.ChangesConfig{Limits: jobinspect.ChangesLimits{PatchBytes: 1}}).OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: q})
			changesCode(t, e, "limit")
			ce := jobinspect.ChangesDiagnostic(context.DeadlineExceeded)
			raw, e := json.Marshal(ce)
			require.NoError(t, e)
			var copy jobinspect.ChangesError
			require.NoError(t, json.Unmarshal(raw, &copy))
			require.True(t, errors.Is(&copy, context.DeadlineExceeded))
		})
	}
}
