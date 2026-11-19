package jobinspect_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/stretchr/testify/require"
)

func TestChangesJ28CommitHeaders(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			for _, format := range []string{"sha1", "sha256"} {
				t.Run(format, func(t *testing.T) {
					for _, fault := range []string{"missing-author", "missing-committer", "invalid-date", "overflow-date", "invalid-zone", "interior-missing-author", "base-missing-author", "valid-extensions"} {
						t.Run(fault, func(t *testing.T) {
							ctx, rt := changesBackend(t, backend)
							dir := t.TempDir()
							checkout := filepath.Join(dir, "checkout")
							git := func(input []byte, args ...string) []byte {
								b, err := changestest.GitInput(ctx, checkout, bytes.NewReader(input), args...)
								require.NoError(t, err)
								return b
							}
							valid := fault == "valid-extensions"
							f, err := changestest.Build(ctx, rt, changestest.Options{Directory: dir, Format: format, Transform: func(i int, s *changestest.Step) {
								if i != 3 {
									return
								}
								target := s.State.HeadCommit
								if fault == "base-missing-author" {
									target = s.State.BaseCommit
								}
								raw := string(git(nil, "cat-file", "commit", target))
								lines := strings.Split(raw, "\n")
								for n, line := range lines {
									switch {
									case strings.HasPrefix(line, "author ") && strings.Contains(fault, "missing-author"), strings.HasPrefix(line, "committer ") && fault == "missing-committer":
										lines = append(lines[:n], lines[n+1:]...)
										goto modified
									case strings.HasPrefix(line, "author ") && fault == "invalid-date":
										lines[n] = "author Fixture <fixture@example.test> invalid +0000"
									case strings.HasPrefix(line, "author ") && fault == "overflow-date":
										lines[n] = "author Fixture <fixture@example.test> 9223372036854775808 +0000"
									case strings.HasPrefix(line, "committer ") && fault == "invalid-zone":
										lines[n] = "committer Fixture <fixture@example.test> 978307200 +00xx"
									}
								}
							modified:
								raw = strings.Join(lines, "\n")
								if valid {
									raw = strings.Replace(raw, "\n\n", "\nencoding UTF-8\ngpgsig fixture signature\n continuation\n \n final continuation\nx-fixture future header\n\n", 1)
								}
								head := strings.TrimSpace(string(git([]byte(raw), "hash-object", "-w", "-t", "commit", "--literally", "--stdin")))
								_, oracleErr := changestest.Git(ctx, checkout, "fsck", "--strict", "--no-dangling", head)
								if valid {
									require.NoError(t, oracleErr)
								} else {
									require.Error(t, oracleErr, "Git must independently reject the negative fixture")
									diagnostic := "missingAuthor"
									switch fault {
									case "missing-committer":
										diagnostic = "missingCommitter"
									case "invalid-date":
										diagnostic = "badDate"
									case "overflow-date":
										diagnostic = "badDateOverflow"
									case "invalid-zone":
										diagnostic = "badTimezone"
									}
									require.Contains(t, oracleErr.Error(), diagnostic)
								}
								if fault == "base-missing-author" || fault == "interior-missing-author" {
									if fault == "base-missing-author" {
										s.State.BaseCommit = head
									}
									tree := strings.TrimSpace(string(git(nil, "rev-parse", s.State.HeadCommit+"^{tree}")))
									head = strings.TrimSpace(string(git(nil, "commit-tree", tree, "-p", head, "-m", "valid successor")))
								}
								objects := git(nil, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
								pack := git(objects, "pack-objects", "--stdout")
								header := fmt.Sprintf("# v2 git bundle\n%s HEAD\n\n", head)
								if format == "sha256" {
									header = fmt.Sprintf("# v3 git bundle\n@object-format=sha256\n%s HEAD\n\n", head)
								}
								s.Bundle = append([]byte(header), pack...)
								s.State.HeadCommit = head
							}})
							require.NoError(t, err)
							state := f.Steps[3].State
							var oracle, singleOracle []byte
							if valid {
								oracle, err = f.Oracle(ctx, state.BaseCommit, state.HeadCommit)
								require.NoError(t, err)
								singleOracle, err = f.Oracle(ctx, f.Commits[1], state.HeadCommit)
								require.NoError(t, err)
							}
							before, err := changestest.Inventory(ctx, rt, f.Job)
							require.NoError(t, err)
							require.NoError(t, f.DeleteCheckout())
							require.NoError(t, os.RemoveAll(f.Origin))
							temp := t.TempDir()
							readOnly := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
							api := changesAPI(t, readOnly, jobinspect.ChangesConfig{TempDir: temp})
							q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Limit: 1}
							h, err := api.History(ctx, q)
							if valid {
								require.NoError(t, err)
								require.True(t, h.Complete)
							} else {
								changesCode(t, err, "invalid_objects")
								require.Nil(t, h, "invalid headers must not expose even the first valid page")
							}
							q.Limit = 0
							for _, read := range []jobinspect.DiffRequest{
								{ChangesRequest: q, All: true},
								{ChangesRequest: q, Commit: state.HeadCommit},
								{ChangesRequest: q, From: state.HeadCommit, Through: state.HeadCommit},
							} {
								meta, body, err := api.OpenDiff(ctx, read)
								if valid {
									require.NoError(t, err)
									patch, err := io.ReadAll(body)
									require.NoError(t, err)
									require.NoError(t, body.Close())
									require.True(t, meta.Complete)
									want := oracle
									if !read.All {
										want = singleOracle
									}
									require.Equal(t, want, patch)
								} else {
									changesCode(t, err, "invalid_objects")
									require.Nil(t, meta)
									require.Nil(t, body)
								}
							}
							entries, err := os.ReadDir(temp)
							require.NoError(t, err)
							require.Empty(t, entries)
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
}
