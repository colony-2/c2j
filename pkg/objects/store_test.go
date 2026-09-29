package objects

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) (*Store, *map[string]jobdb.Artifact) {
	t.Helper()
	artifacts := map[string]jobdb.Artifact{}
	var mu sync.Mutex
	store := NewStore(Config{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, TaskOrdinal: 2, Workdir: t.TempDir(),
		AddArtifact: func(a jobdb.Artifact) error {
			b, err := a.Bytes(context.Background())
			if err != nil {
				return err
			}
			a.Cleanup()
			mu.Lock()
			defer mu.Unlock()
			artifacts[a.Name()] = jobdb.NewArtifactFromBytes(a.Name(), b)
			return nil
		},
		GetArtifact: func(k jobdb.ArtifactKey) (jobdb.Artifact, error) {
			mu.Lock()
			defer mu.Unlock()
			a := artifacts[k.Name]
			if a == nil {
				return nil, fmt.Errorf("missing")
			}
			return a, nil
		},
	})
	return store, &artifacts
}
func TestCheckpointIndependentCopiesAndConcurrentBranches(t *testing.T) {
	ctx := context.Background()
	store, artifacts := testStore(t)
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "rollout.jsonl"), []byte("original"), 0600))
	require.NoError(t, os.Mkdir(filepath.Join(home, "empty"), 0700))
	metadata := map[string]any{"session_id": "same-id"}
	ref, err := store.Publish(ctx, "test.session/v1", metadata, map[string]string{"home": home})
	require.NoError(t, err)
	metadata["session_id"] = "mutated"
	require.NoError(t, os.WriteFile(filepath.Join(home, "rollout.jsonl"), []byte("changed after publish"), 0600))
	refs := make(chan Ref, 8)
	errors := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snapshot, err := store.Open(ctx, ref, "test.session/v1")
			if err != nil {
				errors <- err
				return
			}
			defer snapshot.Close()
			b, err := os.ReadFile(filepath.Join(snapshot.Files["home"], "rollout.jsonl"))
			if err != nil || string(b) != "original" {
				errors <- fmt.Errorf("original changed: %s %v", b, err)
				return
			}
			if string(snapshot.Metadata) != `{"session_id":"same-id"}` {
				errors <- fmt.Errorf("metadata changed")
				return
			}
			if err = os.WriteFile(filepath.Join(snapshot.Files["home"], "rollout.jsonl"), []byte(fmt.Sprint(i)), 0600); err != nil {
				errors <- err
				return
			}
			next, err := store.Publish(ctx, ref.Type, map[string]any{"session_id": "same-id"}, snapshot.Files)
			if err != nil {
				errors <- err
				return
			}
			refs <- next
		}(i)
	}
	wg.Wait()
	close(errors)
	close(refs)
	for err := range errors {
		require.NoError(t, err)
	}
	seen := map[string]bool{}
	for r := range refs {
		require.NotEqual(t, ref.SHA256, r.SHA256)
		seen[r.SHA256] = true
	}
	require.Len(t, seen, 8)
	require.Len(t, *artifacts, 9)
	final, err := store.Open(ctx, ref, ref.Type)
	require.NoError(t, err)
	defer final.Close()
	b, err := os.ReadFile(filepath.Join(final.Files["home"], "rollout.jsonl"))
	require.NoError(t, err)
	require.Equal(t, "original", string(b))
}
func TestCheckpointRejectsWrongIdentityAndCorruption(t *testing.T) {
	ctx := context.Background()
	s, arts := testStore(t)
	ref, err := s.Publish(ctx, "test.session/v1", map[string]any{"id": 1}, nil)
	require.NoError(t, err)
	_, err = s.Open(ctx, ref, "other/v1")
	require.ErrorContains(t, err, "expected object type")
	foreign := ref
	foreign.TenantID = "another"
	_, err = s.Open(ctx, foreign, ref.Type)
	require.ErrorContains(t, err, "another tenant")
	original := (*arts)[ref.Artifact.Name]
	(*arts)[ref.Artifact.Name] = jobdb.NewArtifactFromBytes(ref.Artifact.Name, bytes.Repeat([]byte("x"), int(ref.Artifact.SizeBytes)))
	_, err = s.Open(ctx, ref, ref.Type)
	require.ErrorContains(t, err, "digest mismatch")
	(*arts)[ref.Artifact.Name] = original
	_, err = s.Publish(ctx, ref.Type, map[string]any{"nested": ref}, nil)
	require.ErrorContains(t, err, "cannot contain object references")
	delete(*arts, ref.Artifact.Name)
	_, err = s.Open(ctx, ref, ref.Type)
	require.Error(t, err)
}
func TestCheckpointRejectsLinksAndUnsafeArchivePaths(t *testing.T) {
	ctx := context.Background()
	s, arts := testStore(t)
	dir := t.TempDir()
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "link")))
	_, err := s.Publish(ctx, "test/v1", nil, map[string]string{"home": dir})
	require.ErrorContains(t, err, "link or special")
	for _, entry := range []tar.Header{{Name: "files/home/../../escape", Typeflag: tar.TypeReg}, {Name: "files/home/link", Typeflag: tar.TypeSymlink, Linkname: "/tmp/escape"}, {Name: "/absolute", Typeflag: tar.TypeReg}, {Name: "files/home/link", Typeflag: tar.TypeLink, Linkname: "manifest.json"}} {
		t.Run(entry.Name+fmt.Sprint(entry.Typeflag), func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			m, _ := json.Marshal(manifest{Format: Version, Type: "test/v1", Metadata: json.RawMessage("null"), Files: map[string]bool{}})
			require.NoError(t, tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Size: int64(len(m))}))
			_, err := tw.Write(m)
			require.NoError(t, err)
			require.NoError(t, tw.WriteHeader(&entry))
			require.NoError(t, tw.Close())
			hash := fmt.Sprintf("%x", sha256.Sum256(buf.Bytes()))
			ref := Ref{Format: Version, Type: "test/v1", TenantID: "tenant", SHA256: hash, Artifact: jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 2, Name: ArtifactPrefix + hash + ".tar", SizeBytes: int64(buf.Len())}}
			(*arts)[ref.Artifact.Name] = jobdb.NewArtifactFromBytes(ref.Artifact.Name, buf.Bytes())
			_, err = s.Open(ctx, ref, ref.Type)
			require.Error(t, err)
		})
	}
}
