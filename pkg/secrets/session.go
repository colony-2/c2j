package secrets

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Resolver func(context.Context, []string) (jobdb.SecretResolveResult, error)

// Session contains only one task attempt's transient secret values.
// Cross-task reuse belongs to JobDB; the shared Redactor cannot resolve names.
type Session struct {
	mu           sync.Mutex
	values       map[string]jobdb.SecretValue
	redactor     *Redactor
	ownsRedactor bool
	released     bool
	closed       bool
}

func NewSession() *Session {
	s := newRedactor().NewTask()
	s.ownsRedactor = true
	return s
}
func (*Session) MarshalJSON() ([]byte, error) {
	return nil, errors.New("secret sessions cannot be serialized")
}
func (*Session) String() string   { return "secret task session (redacted)" }
func (*Session) GoString() string { return "secret task session (redacted)" }
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return
	}
	s.released = true
	clear(s.values)
	s.closed = true
	s.redactor.releaseTask()
	if s.ownsRedactor {
		s.redactor.Close()
	}
}

func (s *Session) Ready(names []string) bool {
	if s == nil {
		return len(names) == 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.redactor.isClosed() {
		return false
	}
	return s.ready(names)
}
func (s *Session) ready(names []string) bool {
	for _, name := range names {
		if value, ok := s.values[name]; !ok || expired(value) {
			return false
		}
	}
	return true
}

func (s *Session) Prepare(ctx context.Context, names []string, resolve Resolver) error {
	refs := make([]Reference, len(names))
	for i, name := range names {
		refs[i] = Reference{Name: name}
	}
	return s.PrepareReferences(ctx, refs, resolve)
}

// PrepareReferences batches only the current task's names. Every preparation
// calls the task-context API; JobDB owns execution-scoped caching and coalescing.
func (s *Session) PrepareReferences(ctx context.Context, refs []Reference, resolve Resolver) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.values)
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed || s.redactor.isClosed() {
		return jobdb.ErrExecutionLeaseLost
	}
	names, required := []string{}, []string{}
	seen, strict := map[string]bool{}, map[string]bool{}
	for _, ref := range refs {
		if ref.Name == "" {
			continue
		}
		if !seen[ref.Name] {
			names = append(names, ref.Name)
			seen[ref.Name] = true
		}
		if ref.Default == nil && (!ref.OmitIfUnavailable || ref.Required) && !strict[ref.Name] {
			required = append(required, ref.Name)
			strict[ref.Name] = true
		}
	}
	// Do not leave partially prepared values available following any failure.
	success := false
	defer func() {
		if !success {
			clear(s.values)
		}
	}()
	for len(names) > 0 {
		n := min(len(names), 64)
		result, err := resolve(ctx, names[:n])
		if err != nil {
			if jobdb.IsExecutionLeaseLost(err) {
				s.closed = true
				return jobdb.ErrExecutionLeaseLost
			}
			return errors.New("secret resolution unavailable")
		}
		if s.redactor.isClosed() {
			return jobdb.ErrExecutionLeaseLost
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, name := range names[:n] {
			if value, ok := result.Secrets[name]; ok {
				if err := s.redactor.add(value.Value); err != nil {
					return err
				}
				s.values[name] = value
			}
		}
		names = names[n:]
	}
	if !s.ready(required) {
		return errors.New("required secret unavailable")
	}
	success = true
	return nil
}

func (s *Session) Materialize(v any) (any, error) {
	value, err := Transform(v, s.materializeReference)
	if _, omit := value.(omitted); omit {
		return nil, errors.New("null secret fallback requires an object field")
	}
	return value, err
}

func (s *Session) materializeReference(r Reference) (any, error) {
	if r.Name == "" && r.Default != nil {
		return *r.Default, nil
	}
	if s == nil {
		return nil, errors.New("secret setup required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.redactor.isClosed() {
		return nil, jobdb.ErrExecutionLeaseLost
	}
	value, ok := s.values[r.Name]
	if !ok || expired(value) {
		if r.OmitIfUnavailable && !r.Required {
			return omitted{}, nil
		}
		if r.Default != nil {
			return *r.Default, nil
		}
		return nil, errors.New("required secret unavailable")
	}
	return value.Value, nil
}

func (s *Session) Redact(text string) string {
	if s == nil {
		return text
	}
	return s.redactor.Redact(text)
}
func (s *Session) Error(err error) error {
	if s == nil {
		return err
	}
	return s.redactor.Error(err)
}
func (s *Session) CheckOutput(value any) error {
	var redactor *Redactor
	if s != nil {
		redactor = s.redactor
	}
	return redactor.CheckOutput(value)
}

type contextKey struct{}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, contextKey{}, s)
}
func FromContext(ctx context.Context) *Session { s, _ := ctx.Value(contextKey{}).(*Session); return s }

// Sessions shares redaction tracking across a local job execution. It never
// stores names or values for materializing task inputs.
type Sessions struct {
	mu     sync.Mutex
	active map[jobdb.JobKey]executionRedactor
}

type executionRedactor struct {
	ctx      context.Context
	redactor *Redactor
}

func NewSessions() *Sessions { return &Sessions{active: map[jobdb.JobKey]executionRedactor{}} }
func (m *Sessions) Open(key jobdb.JobKey) (*Redactor, error) {
	return m.OpenExecution(context.Background(), key)
}

// OpenExecution allows recovery once the previous activation is cancelled,
// even while its abandoned callbacks are still unwinding.
func (m *Sessions) OpenExecution(ctx context.Context, key jobdb.JobKey) (*Redactor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prior, ok := m.active[key]; ok && prior.ctx.Err() == nil {
		return nil, errors.New("job already executing locally")
	}
	s := newRedactor()
	m.active[key] = executionRedactor{ctx: ctx, redactor: s}
	registerMask(s)
	return s, nil
}
func (m *Sessions) Get(key jobdb.JobKey) *Redactor {
	return m.GetExecution(context.Background(), key)
}

func (m *Sessions) GetExecution(ctx context.Context, key jobdb.JobKey) *Redactor {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	entry, ok := m.active[key]
	if !ok || entry.ctx.Err() != nil {
		return nil
	}
	return entry.redactor
}
func (m *Sessions) Close(key jobdb.JobKey) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok := m.active[key]; ok {
		entry.redactor.Close()
		delete(m.active, key)
	}
}

// CloseExecution cannot close a newer activation of the same job.
func (m *Sessions) CloseExecution(key jobdb.JobKey, redactor *Redactor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	redactor.Close()
	if m.active[key].redactor == redactor {
		delete(m.active, key)
	}
}

func expired(v jobdb.SecretValue) bool { return v.ExpiresAt != nil && !time.Now().Before(*v.ExpiresAt) }
