package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func changesCLIBackend(t *testing.T, backend string) (context.Context, jobdb.ConsumerRuntime) {
	t.Helper()
	ctx := celltest.Context(context.Background())
	db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "cli.db")})
	require.NoError(t, e)
	t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
	var rt jobdb.ConsumerRuntime = db
	if backend == "http" {
		server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
		t.Cleanup(server.Close)
		rt, e = remote.New(server.URL, server.Client())
		require.NoError(t, e)
	}
	celltest.Create(t, rt, "tenant", "changes", "example.test/changes")
	return ctx, rt
}

func TestChangesCLI_J45Movement(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesCLIBackend(t, backend)
			guard := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
			api := &jobinspect.Service{Runtime: guard, Tenant: "tenant"}
			var old jobinspect.ChangesHistory
			f, e := changestest.Build(ctx, rt, changestest.Options{Directory: t.TempDir(), JobID: "cli-moving", Commits: 4, BeforeTask: func(i int) error {
				if i != 3 && i != 4 {
					return nil
				}
				key := jobdb.JobKey{TenantId: "tenant", JobId: "cli-moving"}
				before, e := changestest.Inventory(ctx, rt, key)
				require.NoError(t, e)
				if i == 3 {
					out, _, e := changesCLI(t, ctx, api, "", "changes", key.JobId, "--json", "--limit", "1")
					require.NoError(t, e)
					require.NoError(t, json.Unmarshal(out, &old))
					require.NotEmpty(t, old.NextCursor)
				} else {
					for _, args := range [][]string{{"changes", key.JobId, "--json", "--limit", "1", "--cursor", old.NextCursor}, {"diff", key.JobId, "--json", "--selection", old.Selection}} {
						out, diag, e := changesCLI(t, ctx, api, "", args...)
						require.Error(t, e)
						require.Empty(t, out)
						var d jobinspect.ChangesError
						require.NoError(t, json.Unmarshal(diag, &d))
						require.Equal(t, "stale_selection", d.Code)
					}
					out, _, e := changesCLI(t, ctx, api, "", "changes", key.JobId, "--json")
					require.NoError(t, e)
					var fresh jobinspect.ChangesHistory
					require.NoError(t, json.Unmarshal(out, &fresh))
					require.NotEqual(t, old.Head, fresh.Head)
					require.NotEqual(t, old.Selection, fresh.Selection)
					_, diag, e := changesCLI(t, ctx, api, "", "changes", "wrong-job", "--json", "--limit", "1", "--cursor", old.NextCursor)
					require.Error(t, e)
					var d jobinspect.ChangesError
					require.NoError(t, json.Unmarshal(diag, &d))
					require.Equal(t, "invalid_cursor", d.Code)
				}
				after, e := changestest.Inventory(ctx, rt, key)
				require.NoError(t, e)
				require.Equal(t, string(before), string(after))
				return nil
			}})
			require.NoError(t, e)
			out, _, e := changesCLI(t, ctx, api, "", "changes", f.Job.JobId, "--json")
			require.NoError(t, e)
			var final jobinspect.ChangesHistory
			require.NoError(t, json.Unmarshal(out, &final))
			require.Equal(t, f.Head, final.Head)
			require.Zero(t, guard.Mutations.Load())
		})
	}
}

func TestChangesCLI_J46FailureDiagnosticsAndUnsupportedAll(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesCLIBackend(t, backend)
			guard := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
			fixtures := []*changestest.Fixture{}
			build := func(opt changestest.Options) *changestest.Fixture {
				opt.Directory = t.TempDir()
				f, e := changestest.Build(ctx, rt, opt)
				require.NoError(t, e)
				fixtures = append(fixtures, f)
				return f
			}
			linear := build(changestest.Options{})
			missing := build(changestest.Options{OmitBaseBundle: true})
			merged := build(changestest.Options{MergeAt: 1})
			oracle, e := merged.Oracle(ctx, merged.Base, merged.Head)
			require.NoError(t, e)
			baselines := map[string][]byte{}
			for _, f := range fixtures {
				require.NoError(t, f.DeleteCheckout())
				before, e := changestest.Inventory(ctx, rt, f.Job)
				require.NoError(t, e)
				baselines[f.Job.JobId] = before
			}
			for _, tc := range []struct {
				f      *changestest.Fixture
				config jobinspect.ChangesConfig
				code   string
			}{
				{linear, jobinspect.ChangesConfig{Limits: jobinspect.ChangesLimits{Commits: 1}}, "limit"},
				{missing, jobinspect.ChangesConfig{}, "missing_closure"},
				{missing, jobinspect.ChangesConfig{ResolveRepository: func(context.Context, string) (jobinspect.RepositoryAccess, error) {
					return jobinspect.RepositoryAccess{}, &jobinspect.ChangesError{Version: 1, Code: "repository_denied"}
				}}, "repository_denied"},
			} {
				api, e := jobinspect.NewChangesService(&jobinspect.Service{Runtime: guard, Tenant: "tenant"}, tc.config)
				require.NoError(t, e)
				for _, command := range []string{"changes", "diff"} {
					out, diag, e := changesCLI(t, ctx, api, "", command, tc.f.Job.JobId, "--json")
					require.Error(t, e)
					require.Empty(t, out)
					var d jobinspect.ChangesError
					require.NoError(t, json.Unmarshal(diag, &d))
					require.Equal(t, tc.code, d.Code)
				}
			}
			api := &jobinspect.Service{Runtime: guard, Tenant: "tenant"}
			out, diag, e := changesCLI(t, ctx, api, "", "changes", merged.Job.JobId, "--json", "--limit", "1")
			require.Error(t, e)
			var history jobinspect.ChangesHistory
			require.NoError(t, json.Unmarshal(out, &history))
			require.False(t, history.Complete)
			require.Empty(t, history.Commits)
			require.Empty(t, history.NextCursor)
			var d jobinspect.ChangesError
			require.NoError(t, json.Unmarshal(diag, &d))
			require.Equal(t, "unsupported_history", d.Code)
			out, diag, e = changesCLI(t, ctx, api, "", "diff", merged.Job.JobId, "--all", "--json")
			require.NoError(t, e)
			require.Equal(t, oracle, out)
			var meta jobinspect.ChangesDiff
			require.NoError(t, json.Unmarshal(diag, &meta))
			require.Equal(t, "unsupported", meta.History)
			require.True(t, meta.Complete)
			for _, args := range [][]string{{"--commit", merged.Commits[0]}, {"--from", merged.Commits[0], "--through", merged.Head}} {
				out, diag, e = changesCLI(t, ctx, api, "", append([]string{"diff", merged.Job.JobId, "--json"}, args...)...)
				require.Error(t, e)
				require.Empty(t, out)
				require.NoError(t, json.Unmarshal(diag, &d))
				require.Equal(t, "unsupported_history", d.Code)
			}
			for _, f := range fixtures {
				after, e := changestest.Inventory(ctx, rt, f.Job)
				require.NoError(t, e)
				require.Equal(t, string(baselines[f.Job.JobId]), string(after))
			}
			require.Zero(t, guard.Mutations.Load())
		})
	}
}

func changesCLI(t *testing.T, ctx context.Context, api jobinspect.ChangesAPI, current string, args ...string) ([]byte, []byte, error) {
	t.Helper()
	var uri string
	var embed, jsonOutput bool
	var out, diagnostic bytes.Buffer
	c := &cobra.Command{Use: "job"}
	c.SetOut(&out)
	c.SetErr(&diagnostic)
	c.SetContext(ctx)
	c.SetArgs(args)
	c.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON")
	c.AddCommand(newJobChangesCommands(&uri, &embed, &jsonOutput, func(context.Context, string) (jobinspect.ChangesAPI, func(), string, error) {
		return api, func() {}, current, nil
	})...)
	err := c.Execute()
	return out.Bytes(), diagnostic.Bytes(), err
}

func TestChangesCLI_J43J44J45J46J47(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "cli.db")})
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
			f, e := changestest.Build(ctx, runtime, changestest.Options{Directory: t.TempDir()})
			require.NoError(t, e)
			oracle, e := f.Oracle(ctx, f.Base, f.Head)
			require.NoError(t, e)
			rangeOracle, e := f.Oracle(ctx, f.Base, f.Commits[1])
			require.NoError(t, e)
			singleOracle, e := f.Oracle(ctx, f.Commits[0], f.Commits[1])
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			readOnly := &changestest.ReadOnlyRuntime{ConsumerRuntime: runtime}
			api := &jobinspect.Service{Runtime: readOnly, Tenant: "tenant"}
			before, e := changestest.Inventory(ctx, runtime, f.Job)
			require.NoError(t, e)
			out, _, e := changesCLI(t, ctx, api, f.Job.JobId, "changes", "self", "--json", "--limit", "1")
			require.NoError(t, e)
			var h jobinspect.ChangesHistory
			require.NoError(t, json.Unmarshal(out, &h))
			require.Equal(t, f.Head, h.Head)
			require.NotEmpty(t, h.State.Task)
			require.Positive(t, h.State.Attempt)
			require.Len(t, h.Commits, 1)
			var hashes []string
			for {
				for _, r := range h.Commits {
					hashes = append(hashes, r.Hash)
				}
				if h.NextCursor == "" {
					break
				}
				out, _, e = changesCLI(t, ctx, api, "", "changes", f.Job.JobId, "--json", "--limit", "1", "--cursor", h.NextCursor)
				require.NoError(t, e)
				h = jobinspect.ChangesHistory{}
				require.NoError(t, json.Unmarshal(out, &h))
			}
			require.Equal(t, f.Commits, hashes)
			for _, tc := range []struct {
				args  []string
				patch []byte
			}{{[]string{"--all"}, oracle}, {[]string{"--commit", f.Commits[1]}, singleOracle}, {[]string{"--from", f.Commits[0], "--through", f.Commits[1]}, rangeOracle}} {
				args := append([]string{"diff", f.Job.JobId, "--json", "--selection", h.Selection}, tc.args...)
				out, diag, e := changesCLI(t, ctx, api, "", args...)
				require.NoError(t, e)
				require.Equal(t, tc.patch, out)
				var meta jobinspect.ChangesDiff
				require.NoError(t, json.Unmarshal(diag, &meta))
				require.Equal(t, h.Selection, meta.Selection)
				require.True(t, meta.Complete)
				path := filepath.Join(t.TempDir(), "diff.patch")
				args = append(args, "--output", path)
				out, _, e = changesCLI(t, ctx, api, "", args...)
				require.NoError(t, e)
				require.Empty(t, out)
				actual, e := os.ReadFile(path)
				require.NoError(t, e)
				require.Equal(t, tc.patch, actual)
				_, _, e = changesCLI(t, ctx, api, "", args...)
				require.Error(t, e)
				actual, e = os.ReadFile(path)
				require.NoError(t, e)
				require.Equal(t, tc.patch, actual)
			}
			out, diag, e := changesCLI(t, ctx, api, "", "diff", f.Job.JobId, "--json", "--selection", "wrong")
			require.Error(t, e)
			require.Empty(t, out)
			var ce jobinspect.ChangesError
			require.NoError(t, json.Unmarshal(diag, &ce))
			require.Equal(t, "stale_selection", ce.Code)
			for _, arg := range []string{"--task", "--attempt", "--workspace", "--review", "--context", "--repo", "--ref"} {
				_, _, e = changesCLI(t, ctx, api, "", "changes", f.Job.JobId, arg, "other")
				require.Error(t, e)
			}
			after, e := changestest.Inventory(ctx, runtime, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
			require.Zero(t, readOnly.Mutations.Load())
		})
	}
}

type incompleteChanges struct{ jobinspect.ChangesAPI }

func (incompleteChanges) OpenDiff(context.Context, jobinspect.DiffRequest) (*jobinspect.ChangesDiff, io.ReadCloser, error) {
	return &jobinspect.ChangesDiff{Bytes: 10}, io.NopCloser(bytes.NewBufferString("short")), nil
}
func TestChangesCLIIncompleteFileRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.patch")
	_, _, e := changesCLI(t, context.Background(), incompleteChanges{}, "", "diff", "job", "--json", "--output", path)
	require.Error(t, e)
	_, e = os.Stat(path)
	require.True(t, os.IsNotExist(e))
}
