package jobinputs

import (
	"context"
	"errors"
	"io"
	"sync"
)

// VerifiedStream owns source, interrupts blocked reads and requires full EOF,
// source close and a final authority check. It is a single-reader stream.
func VerifiedStream(ctx context.Context, source io.ReadCloser, size int64, check func() error) io.ReadCloser {
	s := &stream{ctx: ctx, source: source, size: size, check: check}
	s.stop = context.AfterFunc(ctx, func() { _ = s.closeSource() })
	return s
}

type stream struct {
	ctx         context.Context
	source      io.ReadCloser
	size, count int64
	check       func() error
	terminal    error
	once        sync.Once
	closeErr    error
	closeOnce   sync.Once
	result      error
	stop        func() bool
}

func (s *stream) closeSource() error {
	s.once.Do(func() {
		if e := s.source.Close(); e != nil {
			s.closeErr = Diagnostic("close", "cannot close retained stream", e)
		}
	})
	return s.closeErr
}
func (s *stream) authority() error {
	if e := context.Cause(s.ctx); e != nil {
		return e
	}
	if s.check != nil {
		return s.check()
	}
	return nil
}
func (s *stream) Read(p []byte) (int, error) {
	if s.terminal != nil {
		return 0, s.terminal
	}
	if e := s.authority(); e != nil {
		return s.finish(0, Diagnostic("canceled", "retained stream authority ended", e))
	}
	if s.size < 0 {
		return s.finish(0, Diagnostic("size_mismatch", "negative retained size", nil))
	}
	n, e := s.source.Read(p)
	s.count += int64(n)
	if cause := s.authority(); cause != nil {
		e = Diagnostic("canceled", "retained stream authority ended", cause)
	}
	if s.count > s.size {
		e = Diagnostic("size_mismatch", "retained stream exceeds recorded size", e)
	}
	if e != nil {
		if e == io.EOF {
			if s.count != s.size {
				e = Diagnostic("size_mismatch", "retained stream shorter than recorded size", nil)
			}
		} else {
			e = Diagnostic("read", "retained stream failed", e)
		}
		return s.finish(n, e)
	}
	return n, nil
}
func (s *stream) finish(n int, e error) (int, error) {
	c := s.closeSource()
	if e == io.EOF {
		e = c
		if e == nil {
			e = io.EOF
		}
	} else {
		e = errors.Join(e, c)
	}
	if a := s.authority(); a != nil {
		e = Diagnostic("canceled", "retained stream authority ended", errors.Join(a, e))
	}
	s.stop()
	s.terminal = e
	return n, e
}
func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		s.stop()
		e := s.closeSource()
		if s.terminal != nil && s.terminal != io.EOF {
			s.result = s.terminal
			return
		}
		if a := s.authority(); a != nil {
			s.result = Diagnostic("canceled", "retained stream authority ended", errors.Join(a, e))
			return
		}
		if s.terminal == nil {
			s.result = Diagnostic("read", "retained stream closed before EOF", e)
			return
		}
		s.result = e
	})
	return s.result
}
