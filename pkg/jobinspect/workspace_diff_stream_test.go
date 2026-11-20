package jobinspect

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type workspaceFaultBody struct {
	io.Reader
	closed   atomic.Int32
	closeErr error
}

func (b *workspaceFaultBody) Close() error { b.closed.Add(1); return b.closeErr }

type workspaceTerminalReader struct {
	done bool
	err  error
}

func (r *workspaceTerminalReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	return copy(p, "bytes"), r.err
}

func TestWorkspaceDiffStreamI05E01E02E03M02(t *testing.T) {
	closeFailure := errors.New("close fault")
	readFailure := errors.New("read fault")
	for _, tc := range []struct {
		name, data     string
		size           *int64
		closeErr, want error
		code           string
	}{
		{name: "known", data: "bytes", size: new(int64(5))},
		{name: "unknown", data: "bytes"},
		{name: "zero", size: new(int64(0))},
		{name: "short", data: "four", size: new(int64(5)), code: "byte_count"},
		{name: "extra", data: "bytes!", size: new(int64(5)), code: "byte_count"},
		{name: "close_after_exact_length", data: "bytes", size: new(int64(5)), closeErr: closeFailure, want: closeFailure, code: "close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &workspaceFaultBody{Reader: strings.NewReader(tc.data), closeErr: tc.closeErr}
			body := NewWorkspaceDiffStream(context.Background(), source, tc.size)
			data, err := io.ReadAll(body)
			require.Equal(t, tc.data, string(data))
			if tc.code == "" {
				require.NoError(t, err)
			} else {
				var typed *WorkspaceDiffError
				require.ErrorAs(t, err, &typed)
				require.Equal(t, tc.code, typed.Code)
				if tc.want != nil {
					require.ErrorIs(t, err, tc.want)
				}
			}
			for range 2 {
				closed := body.Close()
				if tc.code == "" {
					require.NoError(t, closed)
				} else {
					var typed *WorkspaceDiffError
					require.ErrorAs(t, closed, &typed)
					require.Equal(t, tc.code, typed.Code)
					if tc.closeErr != nil {
						require.ErrorIs(t, closed, tc.closeErr)
					}
				}
			}
			require.EqualValues(t, 1, source.closed.Load())
		})
	}
	t.Run("bytes_with_error", func(t *testing.T) {
		source := &workspaceFaultBody{Reader: &workspaceTerminalReader{err: readFailure}}
		body := NewWorkspaceDiffStream(context.Background(), source, nil)
		data, err := io.ReadAll(body)
		require.Equal(t, "bytes", string(data))
		require.ErrorIs(t, err, readFailure)
		require.ErrorIs(t, body.Close(), readFailure)
		require.EqualValues(t, 1, source.closed.Load())
	})
	t.Run("preview_close", func(t *testing.T) {
		source := &workspaceFaultBody{Reader: strings.NewReader("preview only"), closeErr: closeFailure}
		body := NewWorkspaceDiffStream(context.Background(), source, new(int64(100)))
		_, err := body.Read(make([]byte, 3))
		require.NoError(t, err)
		for range 2 {
			require.ErrorIs(t, body.Close(), closeFailure)
		}
		require.EqualValues(t, 1, source.closed.Load())
	})
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, jobdb.ErrExecutionLeaseLost} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(context.Background())
			source := &workspaceFaultBody{Reader: strings.NewReader("bytes")}
			body := NewWorkspaceDiffStream(ctx, source, nil)
			cancel(cause)
			_, err := io.ReadAll(body)
			require.ErrorIs(t, err, cause)
			require.ErrorIs(t, body.Close(), cause)
			require.EqualValues(t, 1, source.closed.Load())
		})
	}
}
