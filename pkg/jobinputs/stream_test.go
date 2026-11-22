package jobinputs

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type faultStream struct {
	io.Reader
	closeErr error
	closes   atomic.Int32
}

func (f *faultStream) Close() error { f.closes.Add(1); return f.closeErr }

type dataError struct{ err error }

func (r dataError) Read(p []byte) (int, error) { return copy(p, "abc"), r.err }
func TestInputHistoryVerifiedBytes(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		size       int64
		reader     io.Reader
		close      error
		code       string
	}{
		{name: "empty", size: 0}, {name: "binary", data: "\x00\xff\xc3\xa9\n", size: 5}, {name: "short", data: "ab", size: 3, code: "size_mismatch"}, {name: "excess", data: "abcd", size: 3, code: "size_mismatch"}, {name: "negative", data: "abc", size: -1, code: "size_mismatch"}, {name: "data-plus-error", size: 3, reader: dataError{errors.New("read fault")}, code: "read"}, {name: "close", data: "abc", size: 3, close: errors.New("close fault"), code: "close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.reader
			if r == nil {
				r = strings.NewReader(tc.data)
			}
			source := &faultStream{Reader: r, closeErr: tc.close}
			v := VerifiedStream(context.Background(), source, tc.size, nil)
			b, e := io.ReadAll(v)
			if tc.code == "" {
				require.NoError(t, e)
				require.Equal(t, tc.data, string(b))
				require.NoError(t, v.Close())
			} else {
				var d *Error
				require.ErrorAs(t, e, &d)
				require.Equal(t, tc.code, d.Code)
				require.Error(t, v.Close())
				require.Error(t, v.Close())
			}
			require.Equal(t, int32(1), source.closes.Load())
		})
	}
	// Exact count alone is insufficient; the next read must establish EOF.
	source := &faultStream{Reader: io.MultiReader(strings.NewReader("abc"), dataError{errors.New("late failure")})}
	v := VerifiedStream(context.Background(), source, 3, nil)
	buf := make([]byte, 3)
	n, e := v.Read(buf)
	require.Equal(t, 3, n)
	require.NoError(t, e)
	_, e = io.ReadAll(v)
	require.Error(t, e)
}

type blocked struct {
	started, closed chan struct{}
	once            sync.Once
	closes          atomic.Int32
}

func (b *blocked) Read([]byte) (int, error) {
	close(b.started)
	<-b.closed
	return 0, errors.New("closed while reading")
}
func (b *blocked) Close() error { b.closes.Add(1); b.once.Do(func() { close(b.closed) }); return nil }
func TestInputHistoryBlockedCancellation(t *testing.T) {
	for _, cause := range []error{context.Canceled, jobdb.ErrExecutionLeaseLost} {
		ctx, cancel := context.WithCancelCause(context.Background())
		source := &blocked{started: make(chan struct{}), closed: make(chan struct{})}
		v := VerifiedStream(ctx, source, 3, nil)
		done := make(chan error, 1)
		go func() { _, e := io.ReadAll(v); done <- e }()
		<-source.started
		cancel(cause)
		select {
		case e := <-done:
			require.ErrorIs(t, e, cause)
		case <-time.After(time.Second):
			t.Fatal("blocked read did not stop")
		}
		require.ErrorIs(t, v.Close(), cause)
		require.Equal(t, int32(1), source.closes.Load())
	}
}
func TestInputHistoryFinalAuthority(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	source := &faultStream{Reader: strings.NewReader("abc")}
	v := VerifiedStream(ctx, source, 3, nil)
	p := make([]byte, 3)
	_, e := v.Read(p)
	require.NoError(t, e)
	cancel(jobdb.ErrExecutionLeaseLost)
	_, e = v.Read(p)
	require.ErrorIs(t, e, jobdb.ErrExecutionLeaseLost)
	v = VerifiedStream(context.Background(), &faultStream{Reader: strings.NewReader("abc")}, 3, func() error { return jobdb.ErrExecutionLeaseLost })
	_, e = io.ReadAll(v)
	require.ErrorIs(t, e, jobdb.ErrExecutionLeaseLost)
}
