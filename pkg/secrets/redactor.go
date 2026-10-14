package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Redactor remembers secret material only to prevent disclosure across tasks.
// It has no name-to-value lookup and is cleared when the local execution ends.
type Redactor struct {
	mu     sync.Mutex
	masks  map[string]bool
	closed bool
	tasks  int
}

func newRedactor() *Redactor { return &Redactor{masks: map[string]bool{}} }
func (*Redactor) MarshalJSON() ([]byte, error) {
	return nil, errors.New("secret redactors cannot be serialized")
}
func (*Redactor) String() string   { return "secret redactor (redacted)" }
func (*Redactor) GoString() string { return "secret redactor (redacted)" }
func (s *Redactor) NewTask() *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks++
	return &Session{values: map[string]jobdb.SecretValue{}, redactor: s}
}
func (s *Redactor) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.clearClosed()
}
func (s *Redactor) releaseTask() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks--
	s.clearClosed()
}

// Cancelled tasks may still log while unwinding. Retain their masks until the
// last task releases them, while preventing any new secret materialization.
func (s *Redactor) clearClosed() {
	if s.closed && s.tasks == 0 {
		clear(s.masks)
		activeMasks.Delete(s)
	}
}
func (s *Redactor) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
func (s *Redactor) add(value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return jobdb.ErrExecutionLeaseLost
	}
	s.masks[value] = true
	return nil
}

func (s *Redactor) Redact(text string) string {
	if s == nil {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]string, 0, len(s.masks)*2)
	for v := range s.masks {
		if v != "" {
			values = append(values, v)
			quoted := strconv.Quote(v)
			values = append(values, quoted[1:len(quoted)-1])
			b, _ := json.Marshal(v)
			if len(b) > 2 {
				values = append(values, string(b[1:len(b)-1]))
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		text = strings.ReplaceAll(text, v, "[REDACTED]")
	}
	return text
}

// Errors with secret-bearing messages lose their unwrap chain: retaining it
// would let error-chain diagnostics serialize the original plaintext.
func (s *Redactor) Error(err error) error {
	if err == nil {
		return nil
	}
	safe := s.Redact(err.Error())
	if safe != err.Error() || s.secretErrorCause(err, 0) {
		return errors.New(safe)
	}
	return err
}

func (s *Redactor) secretErrorCause(err error, depth int) bool {
	if err == nil {
		return false
	}
	// Bound pathological unwrap chains from extension/native errors.
	if depth > 32 {
		return true
	}
	if s.Redact(err.Error()) != err.Error() {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if s.secretErrorCause(e, depth+1) {
				return true
			}
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return s.secretErrorCause(wrapped.Unwrap(), depth+1)
	}
	return false
}
func (s *Redactor) CheckOutput(v any) error {
	if Contains(v) {
		return errors.New("secret references are not allowed in outputs")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("cannot validate op output")
	}
	if s.Redact(string(b)) != string(b) {
		return errors.New("op output contains secret material")
	}
	return nil
}
