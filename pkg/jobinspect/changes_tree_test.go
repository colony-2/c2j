package jobinspect_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/stretchr/testify/require"
)

func TestChangesJ28ActualTreeEntryTypes(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			for _, format := range []string{"sha1", "sha256"} {
				t.Run(format, func(t *testing.T) {
					for _, tc := range []struct {
						name, mode, target string
						nested, interior   bool
					}{
						{name: "file-commit", mode: "100644", target: "commit"},
						{name: "file-tree", mode: "100644", target: "tree"},
						{name: "executable-commit", mode: "100755", target: "commit"},
						{name: "symlink-tree", mode: "120000", target: "tree"},
						{name: "directory-commit", mode: "40000", target: "commit"},
						{name: "nested-file-commit", mode: "100644", target: "commit", nested: true},
						{name: "interior-file-commit", mode: "100644", target: "commit", interior: true},
					} {
						t.Run(tc.name, func(t *testing.T) {
							ctx, rt := changesBackend(t, backend)
							dir := t.TempDir()
							checkout := dir + "/checkout"
							git := func(input []byte, args ...string) []byte {
								b, e := changestest.GitInput(ctx, checkout, bytes.NewReader(input), args...)
								require.NoError(t, e)
								return b
							}
							f, e := changestest.Build(ctx, rt, changestest.Options{Directory: dir, Format: format, Commits: 1, Transform: func(i int, s *changestest.Step) {
								if i != 1 {
									return
								}
								parent := s.State.BaseCommit
								target := parent
								if tc.target == "tree" {
									target = strings.TrimSpace(string(git(nil, "rev-parse", parent+"^{tree}")))
								}
								raw, e := hex.DecodeString(target)
								require.NoError(t, e)
								tree := strings.TrimSpace(string(git(append([]byte(tc.mode+" invalid\x00"), raw...), "hash-object", "-w", "-t", "tree", "--literally", "--stdin")))
								if tc.nested {
									raw, e = hex.DecodeString(tree)
									require.NoError(t, e)
									tree = strings.TrimSpace(string(git(append([]byte("40000 nested\x00"), raw...), "hash-object", "-w", "-t", "tree", "--stdin")))
								}
								head := strings.TrimSpace(string(git(nil, "commit-tree", tree, "-p", parent, "-m", "invalid tree entry")))
								if tc.interior {
									validTree := strings.TrimSpace(string(git(nil, "rev-parse", parent+"^{tree}")))
									head = strings.TrimSpace(string(git(nil, "commit-tree", validTree, "-p", head, "-m", "valid successor")))
								}
								// Use the native writer's object repository plus explicit fault objects.
								// Explicit packing avoids Git's traversal silently normalizing the fault.
								objects := git(nil, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
								pack := git(objects, "pack-objects", "--stdout")
								header := fmt.Sprintf("# v2 git bundle\n%s HEAD\n\n", head)
								if format == "sha256" {
									header = fmt.Sprintf("# v3 git bundle\n@object-format=sha256\n%s HEAD\n\n", head)
								}
								s.Bundle = append([]byte(header), pack...)
								s.State.HeadCommit = head
								_, fsckErr := changestest.Git(ctx, checkout, "fsck", "--no-dangling", head)
								require.Error(t, fsckErr, "independent Git must reject the deliberate type mismatch")
								require.Contains(t, fsckErr.Error(), "not a ")
							}})
							require.NoError(t, e)
							before, e := changestest.Inventory(ctx, rt, f.Job)
							require.NoError(t, e)
							require.NoError(t, f.DeleteCheckout())
							require.NoError(t, os.RemoveAll(f.Origin))
							tmp := t.TempDir()
							api := changesAPI(t, rt, jobinspect.ChangesConfig{TempDir: tmp})
							q := jobinspect.ChangesRequest{JobID: f.Job.JobId, Limit: 1}
							h, e := api.History(ctx, q)
							changesCode(t, e, "invalid_objects")
							require.Nil(t, h, "invalid closure cannot expose even page one")
							q.Limit = 0
							head := f.Steps[1].State.HeadCommit
							for _, selection := range []jobinspect.DiffRequest{
								{ChangesRequest: q},
								{ChangesRequest: q, Commit: head},
								{ChangesRequest: q, From: head, Through: head},
							} {
								meta, body, e := api.OpenDiff(ctx, selection)
								changesCode(t, e, "invalid_objects")
								require.Nil(t, meta)
								require.Nil(t, body)
							}
							entries, e := os.ReadDir(tmp)
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
