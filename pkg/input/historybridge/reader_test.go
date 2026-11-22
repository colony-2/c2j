package historybridge

import (
	"context"
	"errors"
	"io"

	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/internal/inputhistorytest"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type descriptor struct {
	name string
	size int64
	open func() (io.ReadCloser, error)
}

func (a descriptor) Name() string                 { return a.name }
func (a descriptor) Size() int64                  { return a.size }
func (a descriptor) Open() (io.ReadCloser, error) { return a.open() }

type readFault struct {
	io.Reader
	readErr, closeErr error
}

func (r *readFault) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	if n > 0 && r.readErr != nil {
		return n, r.readErr
	}
	return n, e
}
func (r *readFault) Close() error { return r.closeErr }
func TestInputHistoryReaderOriginalAndIntegrity(t *testing.T) {
	ctx, f, _ := bridgeFixture(t)
	p := &inputhistorytest.Policy{ConsumerRuntime: f.Runtime}
	r := &reader{rt: p, wait: f.Control, authority: authority()}
	for _, tc := range []struct {
		name        string
		key         jobdb.ArtifactKey
		size        int64
		runtimeName string
		want        string
	}{
		{name: "exact", key: f.Source, size: int64(len(f.Binary)), runtimeName: f.Source.Name},
		{name: "unknown-runtime-size", key: f.Source, size: -1, runtimeName: f.Source.Name},
		{name: "conflicting-size", key: f.Source, size: 999, runtimeName: f.Source.Name, want: "size_mismatch"},
		{name: "conflicting-name", key: f.Source, size: int64(len(f.Binary)), runtimeName: "other", want: "size_mismatch"},
		{name: "negative-retained", key: func() jobdb.ArtifactKey { k := f.Source; k.SizeBytes = -1; return k }(), size: -1, runtimeName: f.Source.Name, want: "size_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.Fault(func(c context.Context, ref jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
				require.Equal(t, ctx, c)
				require.Equal(t, f.Source.JobId, ref.JobKey.JobId)
				require.Equal(t, f.Source.TaskOrdinal, ref.Ordinal)
				require.Equal(t, f.Source.Name, ref.Name)
				return descriptor{name: tc.runtimeName, size: tc.size, open: a.Open}, nil
			})
			stream, e := r.open(ctx, inputhistorytest.Tenant, tc.key)
			if tc.want != "" {
				var d *jobinputs.Error
				require.ErrorAs(t, e, &d)
				require.Equal(t, tc.want, d.Code)
			} else {
				require.NoError(t, e)
				b, e := io.ReadAll(stream)
				require.NoError(t, e)
				require.Equal(t, f.Binary, b)
				require.NoError(t, stream.Close())
			}
		})
	}
	_, e := r.open(ctx, "forged-tenant", f.Source)
	require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
}
func TestInputHistoryPrivateRestoreFailures(t *testing.T) {
	ctx, f, _ := bridgeFixture(t)
	require.NoError(t, f.Advance(ctx, 0))
	require.NoError(t, f.Advance(ctx, 1))
	p := &inputhistorytest.Policy{ConsumerRuntime: f.Runtime}
	for _, kind := range []string{"control", "read", "close", "short", "excess", "descriptor", "denied", "missing", "canceled", "lease"} {
		t.Run(kind, func(t *testing.T) {
			p.Fault(func(c context.Context, ref jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
				if !strings.HasSuffix(ref.Name, "c2j-invocation-input.json") {
					return a, nil
				}
				switch kind {
				case "denied":
					return nil, jobdb.ErrLeasePermissionDenied
				case "missing":
					return nil, jobdb.ErrChapterNotFound
				case "canceled":
					return nil, context.Canceled
				case "lease":
					return nil, jobdb.ErrExecutionLeaseLost
				case "descriptor":
					return descriptor{name: a.Name(), size: a.Size() + 1}, nil
				case "control":
					return a, nil
				}
				return descriptor{name: a.Name(), size: a.Size(), open: func() (io.ReadCloser, error) {
					original, e := a.Open()
					if e != nil {
						return nil, e
					}
					b, e := io.ReadAll(original)
					e = errors.Join(e, original.Close())
					if e != nil {
						return nil, e
					}
					data := string(b)
					if kind == "short" {
						data = data[:len(data)-1]
					}
					if kind == "excess" {
						data += "x"
					}
					r := &readFault{Reader: strings.NewReader(data)}
					if kind == "read" {
						r.readErr = errors.New("private read fault")
					}
					if kind == "close" {
						r.closeErr = errors.New("private close fault")
					}
					return r, nil
				}}, nil
			})
			s, e := New(p, f.Control, authority(), input.HistoryConfig{})
			require.NoError(t, e)
			out, e := s.Inspect(ctx, jobinputs.Request{Version: 1, Operation: "list", JobID: f.Job.JobId})
			if kind == "control" {
				require.NoError(t, e)
				require.Contains(t, string(out.Page), "escaped")
			} else if kind == "missing" {
				require.NoError(t, e)
				require.Contains(t, string(out.Page), "partial")
			} else {
				require.Error(t, e)
				require.Nil(t, out)
			}
		})
	}
}
func TestInputHistoryConcurrentReaderIsolation(t *testing.T) {
	ctx, f, _ := bridgeFixture(t)
	require.NoError(t, f.Advance(ctx, 0))
	require.NoError(t, f.Advance(ctx, 1))
	p := &inputhistorytest.Policy{ConsumerRuntime: f.Runtime}
	type poison struct{}
	p.Fault(func(c context.Context, _ jobdb.ArtifactRef, a jobdb.ArtifactReader) (jobdb.ArtifactReader, error) {
		if c.Value(poison{}) == true {
			return nil, jobdb.ErrLeasePermissionDenied
		}
		return a, nil
	})
	s, e := New(p, f.Control, authority(), input.HistoryConfig{})
	require.NoError(t, e)
	var wg sync.WaitGroup
	for _, deny := range []bool{true, false} {
		wg.Add(1)
		go func(deny bool) {
			defer wg.Done()
			out, e := s.Inspect(context.WithValue(ctx, poison{}, deny), jobinputs.Request{Version: 1, Operation: "list", JobID: f.Job.JobId})
			if deny {
				require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
				require.Nil(t, out)
			} else {
				require.NoError(t, e)
				require.NotNil(t, out)
			}
		}(deny)
	}
	wg.Wait()
}
