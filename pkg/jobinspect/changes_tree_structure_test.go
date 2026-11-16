package jobinspect_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/stretchr/testify/require"
)

func TestChangesJ28TreeStructure(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			for _, format := range []string{"sha1", "sha256"} {
				t.Run(format, func(t *testing.T) {
					for _, fault := range []string{"duplicate", "duplicate-file-directory", "unsorted", "empty-name", "nested-duplicate", "interior-duplicate", "valid-prefix-order"} {
						t.Run(fault, func(t *testing.T) {
							ctx, rt := changesBackend(t, backend)
							dir := t.TempDir()
							git := func(input []byte, args ...string) []byte {
								b, e := changestest.GitInput(ctx, dir+"/checkout", bytes.NewReader(input), args...)
								require.NoError(t, e)
								return b
							}
							valid := fault == "valid-prefix-order"
							f, e := changestest.Build(ctx, rt, changestest.Options{Directory: dir, Format: format, Commits: 1, Transform: func(i int, s *changestest.Step) {
								if i != 1 {
									return
								}
								blob := strings.TrimSpace(string(git([]byte("retained content\n"), "hash-object", "-w", "--stdin")))
								emptyTree := strings.TrimSpace(string(git(nil, "hash-object", "-w", "-t", "tree", "--stdin")))
								entry := func(mode, name, hash string) []byte {
									raw, e := hex.DecodeString(hash)
									require.NoError(t, e)
									return append([]byte(mode+" "+name+"\x00"), raw...)
								}
								body := entry("100644", "duplicate", blob)
								diagnostic := "duplicateEntries"
								switch fault {
								case "duplicate", "nested-duplicate", "interior-duplicate":
									body = append(body, body...)
								case "duplicate-file-directory":
									body = entry("100644", "a", blob)
									body = append(body, entry("100644", "a.c", blob)...)
									body = append(body, entry("40000", "a", emptyTree)...)
								case "unsorted":
									body = append(entry("100644", "z", blob), entry("100644", "a", blob)...)
									diagnostic = "treeNotSorted"
								case "empty-name":
									body = entry("100644", "", blob)
									diagnostic = "empty filename"
								case "valid-prefix-order":
									body = append(entry("100644", "a.c", blob), entry("40000", "a", emptyTree)...)
									body = append(body, entry("120000", "b", blob)...)
									body = append(body, entry("160000", "gitlink", strings.Repeat("f", len(blob)))...)
									body = append(body, entry("100644", "tab\tline\n", blob)...)
								}
								tree := strings.TrimSpace(string(git(body, "hash-object", "-w", "-t", "tree", "--literally", "--stdin")))
								if fault == "nested-duplicate" {
									tree = strings.TrimSpace(string(git(entry("40000", "nested", tree), "hash-object", "-w", "-t", "tree", "--stdin")))
								}
								parent := s.State.BaseCommit
								if fault == "interior-duplicate" {
									parent = s.State.HeadCommit
								}
								head := strings.TrimSpace(string(git(nil, "commit-tree", tree, "-p", parent, "-m", fault)))
								if fault == "interior-duplicate" {
									tree = strings.TrimSpace(string(git(nil, "rev-parse", s.State.BaseCommit+"^{tree}")))
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
								_, fsckErr := changestest.Git(ctx, dir+"/checkout", "fsck", "--strict", "--no-dangling", head)
								if valid {
									require.NoError(t, fsckErr)
								} else {
									require.Error(t, fsckErr)
									require.Contains(t, fsckErr.Error(), diagnostic)
								}
							}})
							require.NoError(t, e)
							head := f.Steps[1].State.HeadCommit
							var oracle []byte
							if valid {
								oracle, e = f.Oracle(ctx, f.Base, head)
								require.NoError(t, e)
							}
							before, e := changestest.Inventory(ctx, rt, f.Job)
							require.NoError(t, e)
							require.NoError(t, f.DeleteCheckout())
							require.NoError(t, os.RemoveAll(f.Origin))
							temp := t.TempDir()
							api := changesAPI(t, rt, jobinspect.ChangesConfig{TempDir: temp})
							q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Limit: 1}
							h, e := api.History(ctx, q)
							if valid {
								require.NoError(t, e)
								require.True(t, h.Complete)
								require.Len(t, h.Commits, 1)
								q.Selection = h.Selection
							} else {
								changesCode(t, e, "invalid_objects")
								require.Nil(t, h, "invalid structure must not expose even the first valid page")
							}
							q.Limit = 0
							for _, read := range []jobinspect.DiffRequest{
								{ChangesRequest: q, All: true},
								{ChangesRequest: q, Commit: head},
								{ChangesRequest: q, From: head, Through: head},
							} {
								meta, body, e := api.OpenDiff(ctx, read)
								if valid {
									require.NoError(t, e)
									patch, e := io.ReadAll(body)
									require.NoError(t, e)
									require.NoError(t, body.Close())
									require.True(t, meta.Complete)
									require.Equal(t, oracle, patch)
								} else {
									changesCode(t, e, "invalid_objects")
									require.Nil(t, meta)
									require.Nil(t, body)
								}
							}
							entries, e := os.ReadDir(temp)
							require.NoError(t, e)
							require.Empty(t, entries)
							after, e := changestest.Inventory(ctx, rt, f.Job)
							require.NoError(t, e)
							require.Equal(t, string(before), string(after))
						})
					}
				})
			}
		})
	}
}
