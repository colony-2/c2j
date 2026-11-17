package jobinspect_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/stretchr/testify/require"
)

// Measure the actual Git transfer independently of the Changes reader. The
// origin contains the base; the selected writer bundle requires that base.
func changesFetchBytes(t *testing.T, ctx context.Context, f *changestest.Fixture, hash string) int64 {
	t.Helper()
	dir := t.TempDir()
	_, err := changestest.Git(ctx, dir, "init", "--bare", "--quiet", "--object-format="+f.Options.Format)
	require.NoError(t, err)
	trace := filepath.Join(t.TempDir(), "received.pack")
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+dir, "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--", f.Origin, hash)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TRACE_PACKFILE=" + trace}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	pack, err := os.ReadFile(trace)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix(pack, []byte("PACK")))
	return int64(len(pack))
}

func TestChangesJ30L07RepositoryDownloadBudget(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			for _, format := range []string{"sha1", "sha256"} {
				t.Run(format, func(t *testing.T) {
					f := changesFixture(t, ctx, rt, changestest.Options{Format: format, Commits: 1, OmitBaseBundle: true})
					oracle, err := f.Oracle(ctx, f.Base, f.Head)
					require.NoError(t, err)
					transfer := changesFetchBytes(t, ctx, f, f.Base)
					artifact := int64(len(f.Steps[len(f.Steps)-1].Bundle))
					t.Logf("writer bundle=%d bytes; independent Git transfer=%d bytes", artifact, transfer)
					require.NoError(t, f.DeleteCheckout())
					before, err := changestest.Inventory(ctx, rt, f.Job)
					require.NoError(t, err)
					for _, budget := range []struct {
						name      string
						remaining int64
					}{
						{"exhausted", 0},
						{"one_byte", 1},
						{"one_short", transfer - 1},
						{"exact", transfer},
						{"spare", transfer + 1},
					} {
						t.Run(budget.name, func(t *testing.T) {
							for _, mode := range []string{"history", "all_diff"} {
								t.Run(mode, func(t *testing.T) {
									tmp := t.TempDir()
									readOnly := &changestest.ReadOnlyRuntime{WorkflowRuntime: rt}
									calls := 0
									api := changesAPI(t, readOnly, jobinspect.ChangesConfig{
										TempDir: tmp,
										Limits:  jobinspect.ChangesLimits{ArtifactBytes: artifact, DownloadBytes: artifact + budget.remaining},
										ResolveRepository: func(_ context.Context, repository string) (jobinspect.RepositoryAccess, error) {
											calls++
											require.Equal(t, f.Options.Repository, repository)
											return jobinspect.RepositoryAccess{URL: f.Origin}, nil
										},
									})
									q := jobinspect.ChangesRequest{JobID: f.Job.JobId}
									var err error
									if mode == "history" {
										h, e := api.History(ctx, q)
										err = e
										if budget.remaining < transfer {
											require.Nil(t, h, "no complete or partial history on budget failure")
										} else {
											require.NoError(t, err)
											require.True(t, h.Complete)
											require.Len(t, h.Commits, 1)
											require.Equal(t, []string{f.Base}, h.RepositoryObjects)
										}
									} else if budget.remaining < transfer {
										m, body, e := api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: q, All: true})
										err = e
										if body != nil {
											_ = body.Close()
										}
										require.Nil(t, m, "no successful diff metadata on budget failure")
										require.Nil(t, body, "no patch on budget failure")
									} else {
										m, patch := readPatch(t, ctx, api, jobinspect.DiffRequest{ChangesRequest: q, All: true})
										require.True(t, m.Complete)
										require.Equal(t, oracle, patch)
										require.Equal(t, []string{f.Base}, m.RepositoryObjects)
									}
									if budget.remaining < transfer {
										changesCode(t, err, "limit")
										require.Equal(t, "download_bytes", jobinspect.ChangesDiagnostic(err).Limit)
									}
									require.Equal(t, 1, calls)
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
				})
			}
		})
	}
}

func TestChangesJ30L07CumulativeRepositoryDownloads(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := changesBackend(t, backend)
			f := changesFixture(t, ctx, rt, changestest.Options{Commits: 1, OmitBaseBundle: true})
			tree, err := changestest.Git(ctx, f.Checkout, "rev-parse", f.Base+"^{tree}")
			require.NoError(t, err)
			commit, err := changestest.Git(ctx, f.Checkout, "commit-tree", strings.TrimSpace(string(tree)), "-m", "unrelated comparison base")
			require.NoError(t, err)
			unrelated := strings.TrimSpace(string(commit))
			_, err = changestest.Git(ctx, f.Checkout, "push", f.Origin, unrelated+":refs/heads/unrelated")
			require.NoError(t, err)
			firstTransfer := changesFetchBytes(t, ctx, f, f.Base)
			artifact := int64(len(f.Steps[len(f.Steps)-1].Bundle))
			require.NoError(t, f.DeleteCheckout())
			before, err := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, err)
			for _, mode := range []string{"history", "all_diff"} {
				for _, bounded := range []bool{false, true} {
					name := mode + "/sufficient"
					limits := jobinspect.ChangesLimits{}
					if bounded {
						name = mode + "/second_transfer_overrun"
						limits = jobinspect.ChangesLimits{ArtifactBytes: artifact, DownloadBytes: artifact + firstTransfer + 1}
					}
					t.Run(name, func(t *testing.T) {
						tmp := t.TempDir()
						calls := 0
						readOnly := &changestest.ReadOnlyRuntime{WorkflowRuntime: rt}
						api := changesAPI(t, readOnly, jobinspect.ChangesConfig{TempDir: tmp, Limits: limits, ResolveRepository: func(context.Context, string) (jobinspect.RepositoryAccess, error) {
							calls++
							return jobinspect.RepositoryAccess{URL: f.Origin}, nil
						}})
						q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Base: unrelated}
						var err error
						if mode == "history" {
							_, err = api.History(ctx, q)
						} else {
							_, body, e := api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: q, All: true})
							if body != nil {
								_ = body.Close()
							}
							err = e
						}
						if bounded {
							changesCode(t, err, "limit")
							require.Equal(t, "download_bytes", jobinspect.ChangesDiagnostic(err).Limit)
						} else {
							// Both transfers succeeded; full-chain validation then
							// correctly rejects the unrelated explicit comparison.
							changesCode(t, err, "invalid_range")
						}
						require.Equal(t, 2, calls, "the second fetch must be reached")
						files, err := os.ReadDir(tmp)
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
