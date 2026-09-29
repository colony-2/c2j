package objects

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Config struct {
	JobKey      jobdb.JobKey
	TaskOrdinal int64
	Workdir     string
	GetArtifact func(jobdb.ArtifactKey) (jobdb.Artifact, error)
	AddArtifact func(jobdb.Artifact) error
}

// Store is invocation-local. Open always produces a fresh writable copy, even
// when called twice on the same reference. It has no session-ID or latest cache.
type Store struct {
	config    Config
	mu        sync.Mutex
	published map[string]Ref
}

func NewStore(config Config) *Store { return &Store{config: config, published: map[string]Ref{}} }

type manifest struct {
	Format   string          `json:"format"`
	Type     string          `json:"type"`
	Metadata json.RawMessage `json:"metadata"`
	Files    map[string]bool `json:"files"` // true denotes a directory
}

type Snapshot struct {
	Ref       Ref               `json:"ref"`
	Metadata  json.RawMessage   `json:"metadata"`
	Files     map[string]string `json:"files"`
	Directory string            `json:"-"`
}

func (s *Snapshot) Close() error { return os.RemoveAll(s.Directory) }

// Publish freezes metadata and files immediately; subsequent source changes
// cannot affect the result. JobDB persists the archive with the task outcome.
func (s *Store) Publish(ctx context.Context, objectType string, metadata any, files map[string]string) (Ref, error) {
	var zero Ref
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := validateType(objectType); err != nil {
		return zero, err
	}
	if s.config.JobKey.TenantId == "" || s.config.JobKey.JobId == "" || s.config.TaskOrdinal <= 0 || s.config.AddArtifact == nil {
		return zero, fmt.Errorf("object publication requires a durable task identity")
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return zero, err
	}
	refs, err := Collect(metadata)
	if err != nil {
		return zero, err
	}
	if len(refs) > 0 {
		return zero, fmt.Errorf("object metadata cannot contain object references in v1")
	}
	m := manifest{Format: Version, Type: objectType, Metadata: raw, Files: map[string]bool{}}
	names := make([]string, 0, len(files))
	for name, source := range files {
		if !validPath(name) || strings.Contains(name, "/") {
			return zero, fmt.Errorf("invalid object part name %q", name)
		}
		info, err := os.Lstat(source)
		if err != nil {
			return zero, err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return zero, fmt.Errorf("object parts must be regular files or directories: %s", source)
		}
		m.Files[name] = info.IsDir()
		names = append(names, name)
	}
	sort.Strings(names)
	// Keep the sealed archive outside the invocation directory: JobDB consumes
	// task artifacts after the executor has cleaned that directory.
	f, err := os.CreateTemp("", "c2j-object-*.tar")
	if err != nil {
		return zero, err
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			os.Remove(f.Name())
		}
	}()
	hash := sha256.New()
	tw := tar.NewWriter(io.MultiWriter(f, hash))
	header, err := json.Marshal(m)
	if err != nil {
		return zero, err
	}
	if len(header) > 1024*1024 {
		return zero, fmt.Errorf("object manifest exceeds 1 MiB")
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(header)), Typeflag: tar.TypeReg}); err != nil {
		return zero, err
	}
	if _, err = tw.Write(header); err != nil {
		return zero, err
	}
	for _, name := range names {
		source := files[name]
		err = filepath.WalkDir(source, func(file string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && !info.IsDir() {
				return fmt.Errorf("object checkpoint contains a link or special file: %s", file)
			}
			rel, err := filepath.Rel(source, file)
			if err != nil {
				return err
			}
			tarName := path.Join("files", name, filepath.ToSlash(rel))
			if !validPath(tarName) {
				return fmt.Errorf("unsupported object file path %q", tarName)
			}
			h := &tar.Header{Name: tarName, Mode: int64(info.Mode().Perm()), Typeflag: tar.TypeReg, Size: info.Size()}
			if info.IsDir() {
				h.Typeflag = tar.TypeDir
				h.Size = 0
			}
			if err = tw.WriteHeader(h); err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			r, err := os.Open(file)
			if err != nil {
				return err
			}
			defer r.Close()
			_, err = io.Copy(tw, r)
			return err
		})
		if err != nil {
			return zero, err
		}
	}
	if err = tw.Close(); err != nil {
		return zero, err
	}
	if err = f.Close(); err != nil {
		return zero, err
	}
	digest := fmt.Sprintf("%x", hash.Sum(nil))
	name := ArtifactPrefix + digest + ".tar"
	art, err := jobdb.NewArtifactFromFile(name, f.Name())
	if err != nil {
		return zero, err
	}
	r := Ref{Format: Version, Type: objectType, TenantID: s.config.JobKey.TenantId, SHA256: digest, Artifact: jobdb.ArtifactKey{JobId: s.config.JobKey.JobId, TaskOrdinal: s.config.TaskOrdinal, Name: name, SizeBytes: art.Size()}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.published[digest]; ok {
		return prior, nil
	}
	if err = s.config.AddArtifact(art); err != nil {
		return zero, err
	}
	s.published[digest] = r
	keep = true
	return r, nil
}

func (s *Store) Open(ctx context.Context, ref Ref, expectedType string) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	if ref.TenantID != s.config.JobKey.TenantId {
		return nil, fmt.Errorf("object belongs to another tenant")
	}
	if expectedType != "" && ref.Type != expectedType {
		return nil, fmt.Errorf("expected object type %q, got %q", expectedType, ref.Type)
	}
	if s.config.GetArtifact == nil {
		return nil, fmt.Errorf("object artifact resolver is unavailable")
	}
	art, err := s.config.GetArtifact(ref.Artifact)
	if err != nil {
		return nil, err
	}
	if art == nil {
		return nil, fmt.Errorf("object artifact missing")
	}
	root := filepath.Join(s.config.Workdir, "objects")
	if s.config.Workdir == "" {
		return nil, fmt.Errorf("object workdir is required")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(root, "checkpoint-")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	f, err := os.CreateTemp(root, "archive-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	if err = art.WriteTo(ctx, io.MultiWriter(f, hash)); err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() != ref.Artifact.SizeBytes {
		return nil, fmt.Errorf("object checkpoint size mismatch")
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != ref.SHA256 {
		return nil, fmt.Errorf("object checkpoint digest mismatch")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return nil, err
	}
	tr := tar.NewReader(f)
	first, err := tr.Next()
	if err != nil {
		return nil, err
	}
	if first.Name != "manifest.json" || first.Typeflag != tar.TypeReg || first.Size > 1024*1024 {
		return nil, fmt.Errorf("invalid object manifest")
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		return nil, err
	}
	var m manifest
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return nil, err
	}
	if m.Format != Version || m.Type != ref.Type {
		return nil, fmt.Errorf("object manifest type or version mismatch")
	}
	seen := map[string]bool{}
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if !validPath(h.Name) || !strings.HasPrefix(h.Name, "files/") || seen[h.Name] {
			return nil, fmt.Errorf("invalid or duplicate object entry %q", h.Name)
		}
		seen[h.Name] = true
		target := filepath.Join(dir, filepath.FromSlash(h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(target, 0700)
		case tar.TypeReg:
			if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return nil, err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(h.Mode)&0777|0600)
			if err != nil {
				return nil, err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return nil, copyErr
			}
			err = closeErr
		default:
			return nil, fmt.Errorf("object checkpoint contains a link or special entry")
		}
		if err != nil {
			return nil, err
		}
	}
	paths := map[string]string{}
	for name, isDir := range m.Files {
		if !validPath(name) || strings.Contains(name, "/") {
			return nil, fmt.Errorf("invalid object part %q", name)
		}
		p := filepath.Join(dir, "files", name)
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if info.IsDir() != isDir {
			return nil, fmt.Errorf("object part kind mismatch")
		}
		paths[name] = p
	}
	keep = true
	return &Snapshot{Ref: ref, Metadata: m.Metadata, Files: paths, Directory: dir}, nil
}

func validPath(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "\\\x00:") && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "../") && path.Clean(name) == name
}
