// This disposable test server is compiled separately from the c2j client.
// It exercises the published JobDB transport, without changing c2j's runtime pin.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
)

type policy struct {
	DenyJob, FaultJob, Fault string
	Wrapped, OpenDenied      bool
}

type runtime struct {
	*sqlite.Runtime
	mu sync.Mutex
	p  policy
}

func (r *runtime) selected() policy { r.mu.Lock(); defer r.mu.Unlock(); return r.p }
func denial(p policy) error {
	if p.Wrapped {
		return fmt.Errorf("private authorization detail: %w", jobdb.ErrLeasePermissionDenied)
	}
	return jobdb.ErrLeasePermissionDenied
}
func (r *runtime) GetJob(ctx context.Context, key jobdb.JobKey) (jobdb.JobInfo, error) {
	p := r.selected()
	if key.JobId == p.DenyJob && !p.OpenDenied {
		return jobdb.JobInfo{}, denial(p)
	}
	return r.Runtime.GetJob(ctx, key)
}
func (r *runtime) OpenArtifact(ctx context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	a, err := r.Runtime.OpenArtifact(ctx, ref)
	if err != nil {
		return nil, err
	}
	p := r.selected()
	return descriptor{ArtifactReader: a, p: p, key: ref.JobKey}, nil
}

type descriptor struct {
	jobdb.ArtifactReader
	p   policy
	key jobdb.JobKey
}

func (a descriptor) Open() (io.ReadCloser, error) {
	if a.key.JobId == a.p.DenyJob {
		return nil, denial(a.p)
	}
	s, err := a.ArtifactReader.Open()
	if err != nil || a.key.JobId != a.p.FaultJob {
		return s, err
	}
	return &source{ReadCloser: s, kind: a.p.Fault}, nil
}

type source struct {
	io.ReadCloser
	kind string
}

func (s *source) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	if n > 0 {
		switch s.kind {
		case "read":
			return n, errors.New("source read failed after exact bytes")
		case "short":
			return n - 1, io.EOF
		}
	}
	return n, err
}
func (s *source) Close() error {
	err := s.ReadCloser.Close()
	if s.kind == "close" {
		return errors.Join(err, errors.New("source cleanup failed after exact bytes"))
	}
	return err
}

func run() error {
	root, err := os.MkdirTemp("", "c2j-history-origin-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(root, "retained.db"), Projects: celltest.UnrestrictedProjects{}, MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	rt := &runtime{Runtime: db}
	server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
	defer server.Close()
	out := json.NewEncoder(os.Stdout)
	if err := out.Encode(map[string]string{"url": server.URL}); err != nil {
		return err
	}
	// Test-only policy control uses the private parent pipe, never an HTTP route.
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var p policy
		if err := json.Unmarshal(scanner.Bytes(), &p); err != nil {
			return err
		}
		rt.mu.Lock()
		rt.p = p
		rt.mu.Unlock()
		if err := out.Encode(map[string]bool{"ok": true}); err != nil {
			return err
		}
	}
	return scanner.Err()
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
