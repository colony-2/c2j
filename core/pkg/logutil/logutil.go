package logutil

import (
	"errors"
	"fmt"
	"runtime"
)

// Stacktrace returns a best-effort stack trace for the current goroutine as
// a slice of "file:line function" strings.
func Stacktrace(skip int) []string {
	pcs := make([]uintptr, 64)
	n := runtime.Callers(skip, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	out := make([]string, 0, n)
	for {
		frame, more := frames.Next()
		out = append(out, fmt.Sprintf("%s:%d %s", frame.File, frame.Line, frame.Function))
		if !more {
			break
		}
	}
	return out
}

// ErrorChain returns the error and its unwrap chain as a slice of "type: message"
// strings. Supports multi-errors via Unwrap() []error (e.g. errors.Join).
func ErrorChain(err error) []string {
	if err == nil {
		return nil
	}

	type multiUnwrapper interface {
		Unwrap() []error
	}

	seen := map[error]struct{}{}
	var out []string
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if _, ok := seen[e]; ok {
			out = append(out, fmt.Sprintf("%T: (cycle)", e))
			return
		}
		seen[e] = struct{}{}
		out = append(out, fmt.Sprintf("%T: %v", e, e))

		var multi multiUnwrapper
		if errors.As(e, &multi) {
			for _, inner := range multi.Unwrap() {
				walk(inner)
			}
			return
		}

		walk(errors.Unwrap(e))
	}
	walk(err)
	return out
}
