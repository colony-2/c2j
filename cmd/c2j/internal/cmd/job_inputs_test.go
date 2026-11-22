package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type stagedFault struct {
	*os.File
	mode string
}

func (f stagedFault) Write(p []byte) (int, error) {
	if f.mode == "write" {
		return 0, errors.New("disk full")
	}
	return f.File.Write(p)
}
func (f stagedFault) Close() error {
	e := f.File.Close()
	if f.mode == "close" {
		return errors.Join(e, errors.New("file close fault"))
	}
	return e
}

type atomicSource struct {
	io.Reader
	closeErr error
	once     sync.Once
	closed   error
}

func (s *atomicSource) Close() error { s.once.Do(func() { s.closed = s.closeErr }); return s.closed }

type commitReader struct {
	io.Reader
	dest string
	t    *testing.T
}

func (r commitReader) Read(p []byte) (int, error) {
	_, e := os.Lstat(r.dest)
	require.True(r.t, os.IsNotExist(e), "destination appeared while streaming")
	return r.Reader.Read(p)
}
func TestInputHistoryAtomicFiles(t *testing.T) {
	for _, mode := range []string{"control", "write", "close", "commit", "concurrent", "source-close", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "result")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := &atomicSource{Reader: commitReader{Reader: strings.NewReader("complete\x00\xff雪"), dest: path, t: t}}
			if mode == "source-close" {
				source.closeErr = errors.New("source close fault")
			}
			if mode == "canceled" {
				cancel()
			}
			ops := inputFileOps{create: func(dir string) (inputStage, error) {
				f, e := os.CreateTemp(dir, ".c2j-input-*")
				return stagedFault{f, mode}, e
			}, link: func(from, to string) error {
				if mode == "commit" {
					return errors.New("unsupported no-replace commit")
				}
				if mode == "concurrent" {
					require.NoError(t, os.WriteFile(to, []byte("other writer"), 0600))
				}
				return os.Link(from, to)
			}, remove: os.Remove}
			e := writeInputFileWith(ctx, path, source, ops)
			if mode == "control" {
				require.NoError(t, e)
				b, e := os.ReadFile(path)
				require.NoError(t, e)
				require.Equal(t, "complete\x00\xff雪", string(b))
				info, e := os.Stat(path)
				require.NoError(t, e)
				require.Equal(t, os.FileMode(0600), info.Mode().Perm())
			} else {
				require.Error(t, e)
				if mode == "concurrent" {
					b, e := os.ReadFile(path)
					require.NoError(t, e)
					require.Equal(t, "other writer", string(b))
				} else {
					_, e := os.Lstat(path)
					require.True(t, os.IsNotExist(e))
				}
			}
			files, e := filepath.Glob(filepath.Join(dir, ".c2j-input-*"))
			require.NoError(t, e)
			require.Empty(t, files)
		})
	}
}
func TestInputHistoryCLIValidation(t *testing.T) {
	for _, args := range [][]string{{"list", "self", "--limit", "0"}, {"list", "self", "--limit", "501"}, {"show", "self"}, {"artifact", "self", "--snapshot", "s", "--entry", "e", "--member", "m", "--output", "-"}, {"list"}, {"list", "self", "--role", "agent"}} {
		root := newJobCmd()
		var out, diag strings.Builder
		root.SetOut(&out)
		root.SetErr(&diag)
		root.SetArgs(append([]string{"inputs"}, args...))
		e := root.Execute()
		require.Error(t, e)
		require.Empty(t, out.String())
		require.Contains(t, diag.String(), `"version":1`)
		require.Contains(t, diag.String(), `"code":"invalid_request"`)
	}
}
