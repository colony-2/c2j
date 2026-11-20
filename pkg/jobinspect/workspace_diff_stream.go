package jobinspect

import (
	"context"
	"errors"
	"io"
	"sync"
)

// NewWorkspaceDiffStream counts original bytes, checks EOF and owns the source.
// Close is idempotent and preserves the first close error. An early Close only
// releases the stream; it does not assert complete-file integrity.
func NewWorkspaceDiffStream(ctx context.Context, source io.ReadCloser, size *int64) io.ReadCloser {
	b := &workspaceDiffStream{ctx: ctx, source: source, size: size}
	b.stop = context.AfterFunc(ctx, func() { _ = b.closeSource() })
	return b
}

type workspaceDiffStream struct {
	ctx      context.Context
	source   io.ReadCloser
	size     *int64
	count    int64
	mu       sync.Mutex
	terminal error
	once     sync.Once
	closeErr error // published by once
	stop     func() bool
}

func (b *workspaceDiffStream) terminalError() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.terminal
}

func (b *workspaceDiffStream) setTerminal(err error) {
	b.mu.Lock()
	b.terminal = err
	b.mu.Unlock()
}

func (b *workspaceDiffStream) closeSource() error {
	b.once.Do(func() {
		if err := b.source.Close(); err != nil {
			b.closeErr = WorkspaceDiffDiagnostic("close", "cannot close saved patch stream", err)
		}
	})
	return b.closeErr
}

func (b *workspaceDiffStream) Read(p []byte) (int, error) {
	if err := b.terminalError(); err != nil {
		return 0, err
	}
	if err := context.Cause(b.ctx); err != nil {
		_ = b.closeSource()
		err = WorkspaceDiffDiagnostic("canceled", "saved patch read canceled", err)
		b.setTerminal(err)
		return 0, err
	}
	n, err := b.source.Read(p)
	b.count += int64(n)
	if cause := context.Cause(b.ctx); cause != nil {
		err = WorkspaceDiffDiagnostic("canceled", "saved patch read canceled", cause)
	}
	if b.size != nil && b.count > *b.size {
		err = WorkspaceDiffDiagnostic("byte_count", "saved patch exceeds its recorded byte count", err)
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			if b.size != nil && b.count != *b.size {
				err = WorkspaceDiffDiagnostic("byte_count", "saved patch is shorter than its recorded byte count", nil)
			}
		} else {
			err = WorkspaceDiffDiagnostic("read", "saved patch stream failed", err)
		}
		closeErr := b.closeSource()
		if closeErr != nil {
			if errors.Is(err, io.EOF) {
				err = closeErr
			} else {
				err = errors.Join(err, closeErr)
			}
		}
		b.setTerminal(err)
		b.stop()
	}
	return n, err
}

func (b *workspaceDiffStream) Close() error {
	b.stop()
	err := b.closeSource()
	if cause := context.Cause(b.ctx); cause != nil {
		return errors.Join(WorkspaceDiffDiagnostic("canceled", "saved patch read canceled", cause), err)
	}
	if terminal := b.terminalError(); terminal != nil && !errors.Is(terminal, io.EOF) {
		return terminal
	}
	return err
}
