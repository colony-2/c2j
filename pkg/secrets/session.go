package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Resolver func(context.Context, []string) (jobdb.SecretResolveResult, error)

// Session belongs to one local invocation of a job, including its task retries.
// It is deliberately neither serializable nor a process-wide credential cache.
type Session struct {
	mu     sync.Mutex
	fetch  chan struct{}
	values map[string]jobdb.SecretValue
	masks  map[string]bool
	closed bool
}

func NewSession() *Session {
	return &Session{fetch: make(chan struct{}, 1), values: map[string]jobdb.SecretValue{}, masks: map[string]bool{}}
}
func (*Session) MarshalJSON() ([]byte, error) {
	return nil, errors.New("secret sessions cannot be serialized")
}
func (*Session) String() string   { return "secret session (redacted)" }
func (*Session) GoString() string { return "secret session (redacted)" }
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.values)
	clear(s.masks)
	s.closed = true
}

func (s *Session) Ready(names []string) bool {
	if len(names) == 0 {
		return true
	}
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	for _, n := range names {
		if v, ok := s.values[n]; !ok || expired(v) {
			return false
		}
	}
	return true
}

// Prepare serializes fills so concurrent callers reuse an in-flight batch. The
// small fixed batch also bounds request size for JobDB's default limits. Provider
// response limits remain authoritative; speculative failures fall back to the
// current task's names. Missing future names are never negatively cached.
func (s *Session) Prepare(ctx context.Context, required, candidates []string, resolve Resolver) error {
	return s.prepare(ctx, required, required, candidates, resolve)
}

// PrepareReferences fetches every referenced name, but requires availability only
// for bindings without a fallback. Fallbacks never enter the session cache.
func (s *Session) PrepareReferences(ctx context.Context, refs []Reference, candidates []string, resolve Resolver) error {
	names, required := []string{}, []string{}
	seen, strict := map[string]bool{}, map[string]bool{}
	for _, r := range refs {
		if r.Name == "" {
			continue
		}
		if !seen[r.Name] {
			names = append(names, r.Name)
			seen[r.Name] = true
		}
		if r.Default == nil && !strict[r.Name] {
			required = append(required, r.Name)
			strict[r.Name] = true
		}
	}
	return s.prepare(ctx, names, required, candidates, resolve)
}

func (s *Session) prepare(ctx context.Context, names, required, candidates []string, resolve Resolver) error {
	select {
	case s.fetch <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.fetch }()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return jobdb.ErrExecutionLeaseLost
	}
	missing := []string{}
	for _, n := range names {
		if v, ok := s.values[n]; !ok || expired(v) {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := append([]string(nil), missing...)
	seen := map[string]bool{}
	for _, n := range batch {
		seen[n] = true
	}
	for _, n := range candidates {
		if len(batch) >= 64 {
			break
		}
		if v, ok := s.values[n]; (!ok || expired(v)) && !seen[n] && namePattern.MatchString(n) {
			batch = append(batch, n)
			seen[n] = true
		}
	}
	s.mu.Unlock()
	load := func(names []string) error {
		for len(names) > 0 {
			n := min(len(names), 64)
			result, err := resolve(ctx, names[:n])
			if err != nil {
				return err
			}
			s.mu.Lock()
			if s.closed || ctx.Err() != nil {
				s.mu.Unlock()
				return jobdb.ErrExecutionLeaseLost
			}
			for _, name := range names[:n] {
				if v, ok := result.Secrets[name]; ok {
					s.values[name] = v
					s.masks[v.Value] = true
				}
			}
			s.mu.Unlock()
			names = names[n:]
		}
		return nil
	}
	err := load(batch)
	if err != nil && len(batch) > len(missing) && !jobdb.IsExecutionLeaseLost(err) && ctx.Err() == nil {
		err = load(missing)
	}
	if err != nil {
		if jobdb.IsExecutionLeaseLost(err) {
			s.Close()
			return jobdb.ErrExecutionLeaseLost
		}
		return errors.New("secret resolution unavailable")
	}
	if !s.Ready(required) {
		return errors.New("required secret unavailable")
	}
	return nil
}

func (s *Session) Materialize(v any) (any, error) {
	return Transform(v, func(r Reference) (any, error) {
		if r.Name == "" && r.Default != nil {
			return *r.Default, nil
		}
		if s == nil {
			return nil, errors.New("secret setup required")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return nil, jobdb.ErrExecutionLeaseLost
		}
		value, ok := s.values[r.Name]
		if !ok || expired(value) {
			if r.Default != nil {
				return *r.Default, nil
			}
			return nil, errors.New("required secret unavailable")
		}
		return value.Value, nil
	})
}

func (s *Session) Redact(text string) string {
	if s == nil {
		return text
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	values := make([]string, 0, len(s.values)*2)
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
func (s *Session) Error(err error) error {
	if err == nil {
		return nil
	}
	safe := s.Redact(err.Error())
	if safe != err.Error() || s.secretErrorCause(err, 0) {
		return errors.New(safe)
	}
	return err
}

func (s *Session) secretErrorCause(err error, depth int) bool {
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
func (s *Session) CheckOutput(v any) error {
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

type contextKey struct{}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, contextKey{}, s)
}
func FromContext(ctx context.Context) *Session { s, _ := ctx.Value(contextKey{}).(*Session); return s }

// Sessions connects a WorkSet's job and task workers, without putting a cache ID
// or value into a chapter. A second local execution cannot borrow the first's
// cache; overlapping executions of the same key fail closed.
type Sessions struct {
	mu     sync.Mutex
	active map[jobdb.JobKey]*Session
}

func NewSessions() *Sessions { return &Sessions{active: map[jobdb.JobKey]*Session{}} }
func (m *Sessions) Open(key jobdb.JobKey) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active[key] != nil {
		return nil, errors.New("job already executing locally")
	}
	s := NewSession()
	m.active[key] = s
	registerMask(s)
	return s, nil
}
func (m *Sessions) Get(key jobdb.JobKey) *Session {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[key]
}
func (m *Sessions) Close(key jobdb.JobKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.active[key]; s != nil {
		s.Close()
		activeMasks.Delete(s)
		delete(m.active, key)
	}
}

func expired(v jobdb.SecretValue) bool { return v.ExpiresAt != nil && !time.Now().Before(*v.ExpiresAt) }
