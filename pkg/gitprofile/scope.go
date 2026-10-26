package gitprofile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const SocketEnv = "C2J_EXECUTION_SOCKET"

type Resolver func(context.Context, []string) (jobdb.SecretResolveResult, error)
type Request struct {
	Operation string `json:"operation"`
	Name      string `json:"name,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	Host      string `json:"host,omitempty"`
	Path      string `json:"path,omitempty"`
	Username  string `json:"username,omitempty"`
}
type Response struct {
	Value    json.RawMessage    `json:"value,omitempty"`
	Secret   *jobdb.SecretValue `json:"secret,omitempty"`
	Username string             `json:"username,omitempty"`
	Error    string             `json:"error,omitempty"`
}

type Scope struct {
	ctx          context.Context
	cancel       context.CancelFunc
	dir          string
	namespace    string
	server       *http.Server
	mu           sync.Mutex
	active       bool
	cleared      bool
	generation   uint64
	name         string
	profile      Profile
	snapshot     jobdb.ConfigurationSnapshot
	resolve      Resolver
	selection    Selection
	additional   map[string]bool
	redactor     *secrets.Redactor
	ownsRedactor bool
	once         sync.Once
}

func NewScope(ctx context.Context, selection Selection, snapshot jobdb.ConfigurationSnapshot, resolve Resolver, executable string, namespace ...string) (*Scope, error) {
	if limit, ok := ctx.Value(secretLimitKey{}).([]string); ok && resolve != nil {
		inner := resolve
		resolve = func(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
			for _, name := range names {
				if !slices.Contains(limit, name) {
					return jobdb.SecretResolveResult{}, jobdb.ErrLeasePermissionDenied
				}
			}
			return inner(ctx, names)
		}
	}
	name, err := selection.Resolve(snapshot)
	if err != nil {
		return nil, err
	}
	var p Profile
	if name != "" {
		p, err = Load(snapshot, name)
		if err != nil {
			return nil, err
		}
	}
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "c2j-git-")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Scope{ctx: ctx, cancel: cancel, dir: dir, name: name, profile: p, snapshot: snapshot, resolve: resolve, selection: selection}
	s.namespace = dir
	if len(namespace) > 0 {
		s.namespace = namespace[0]
	}
	s.redactor = secrets.RedactorFromContext(ctx)
	if s.redactor == nil {
		s.redactor = secrets.NewRedactor()
		s.ownsRedactor = true
		s.ctx = secrets.WithRedactor(ctx, s.redactor)
	}
	helper := "'" + strings.ReplaceAll(executable, "'", "'\\''") + "' secret git-credential"
	// A leading ! makes the shell quoting unambiguous even for paths with spaces.
	credential := "[credential]\n\thelper =\n\thelper = " + strconv.Quote("!"+helper) + "\n\tuseHttpPath = true\n[core]\n\taskPass =\n[http]\n\textraHeader =\n"
	var rewrites strings.Builder
	for _, entry := range p.Entries {
		for _, from := range entry.RewriteFrom {
			fmt.Fprintf(&rewrites, "[url %s]\n\tinsteadOf = %s\n", strconv.Quote(entry.URL), strconv.Quote(from))
		}
	}
	for file, contents := range map[string]string{"credentials.gitconfig": credential, "rewrites.gitconfig": rewrites.String()} {
		if err = os.WriteFile(filepath.Join(dir, file), []byte(contents), 0600); err != nil {
			s.Close()
			return nil, err
		}
	}
	if err = s.setActive(name != ""); err != nil {
		s.Close()
		return nil, err
	}
	l, err := net.Listen("unix", filepath.Join(dir, "resolver.sock"))
	if err != nil {
		s.Close()
		return nil, err
	}
	s.server = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = s.server.Serve(l) }()
	go func() { <-ctx.Done(); s.Close() }()
	return s, nil
}
func (s *Scope) setActive(active bool) error {
	// Caller holds mu except during construction. Replace one include file atomically.
	contents := "[include]\n\tpath = " + strconv.Quote(filepath.Join(s.dir, "credentials.gitconfig")) + "\n"
	if active {
		contents += "\tpath = " + strconv.Quote(filepath.Join(s.dir, "rewrites.gitconfig")) + "\n"
	}
	name := filepath.Join(s.dir, "active.gitconfig")
	if err := os.WriteFile(name+".tmp", []byte(contents), 0600); err != nil {
		return err
	}
	if err := os.Rename(name+".tmp", name); err != nil {
		return err
	}
	s.active = active
	s.generation++
	return nil
}
func (s *Scope) Close() {
	s.once.Do(func() {
		s.cancel()
		s.mu.Lock()
		s.active = false
		s.generation++
		s.mu.Unlock()
		if s.server != nil {
			_ = s.server.Close()
		}
		if s.ownsRedactor {
			s.redactor.Close()
		}
		_ = os.RemoveAll(s.dir)
	})
}

// Context carries this invocation's dependencies and cancellation directly.
func (s *Scope) Context() context.Context { return WithScope(s.ctx, s) }

func (s *Scope) CheckOutput(v any) error {
	if raw, ok := v.([]byte); ok {
		v = json.RawMessage(raw)
	}
	return s.redactor.CheckOutput(v)
}
func (s *Scope) Error(err error) error     { return s.redactor.Error(err) }
func (s *Scope) Redact(text string) string { return s.redactor.Redact(text) }

// RetainRedaction keeps masks until a running task finishes unwinding, even
// when cancellation has already closed its resolver socket.
func (s *Scope) RetainRedaction() func() {
	session := s.redactor.NewSession()
	return session.Close
}

// AllowSecrets adds references already validated against the operation schema.
func (s *Scope) AllowSecrets(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.additional == nil {
		s.additional = map[string]bool{}
	}
	for _, name := range names {
		s.additional[name] = true
	}
}

// Fork is for trusted recipe preparation, not the subprocess control API.
func (s *Scope) Fork(ctx context.Context, selection Selection) (*Scope, error) {
	return NewScope(ctx, selection, s.snapshot, s.resolve, "", s.namespace)
}

// CacheNamespace prevents source sharing across tenants and routing profiles.
func CacheNamespace(ctx context.Context) string {
	s := FromContext(ctx)
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	profile := s.profile
	if !s.active {
		profile = Profile{}
	}
	raw, _ := json.Marshal(profile)
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte(s.namespace+"\x00"), raw...)))
}

func (s *Scope) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost || r.URL.Path != "/resolve" {
		http.NotFound(w, r)
		return
	}
	var req Request
	d := json.NewDecoder(io.LimitReader(r.Body, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&req); err != nil {
		_ = json.NewEncoder(w).Encode(Response{Error: "invalid execution request"})
		return
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		_ = json.NewEncoder(w).Encode(Response{Error: "invalid execution request"})
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	s.mu.Lock()
	generation := s.generation
	active := s.active
	additional := s.additional[req.Name]
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	if req.Operation == "clear" || req.Operation == "activate" {
		var err error
		if req.Operation == "activate" && (req.Name != s.name || s.name == "") {
			err = errors.New("profile is not authorized for this scope")
		} else {
			err = s.setActive(req.Operation == "activate")
			if err == nil {
				s.cleared = req.Operation == "clear"
			}
		}
		res := Response{}
		if err != nil {
			res.Error = "profile transition failed or is unauthorized"
		}
		_ = json.NewEncoder(w).Encode(res)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	res := Response{}
	switch req.Operation {
	case "config":
		res.Value = s.snapshot.Values[req.Name]
		if res.Value == nil {
			res.Error = "configuration name is absent"
		}
	case "secret", "credential":
		name := req.Name
		if req.Operation == "credential" {
			e, err := s.profile.Match(req.Protocol, req.Host, req.Path, req.Username)
			if err != nil {
				res.Error = "credential destination is not allowed"
			} else {
				name = e.SecretRef
				res.Username = e.Username
			}
		}
		allowed := false
		for _, e := range s.profile.Entries {
			if e.SecretRef != "" && e.SecretRef == name {
				allowed = true
			}
		}
		if !(active && allowed) && !(req.Operation == "secret" && additional) {
			res.Error = "secret is not authorized for this scope"
		}
		if res.Error == "" {
			if s.resolve == nil {
				res.Error = "secret resolution unavailable"
			} else {
				result, err := s.resolve(ctx, []string{name})
				value, ok := result.Secrets[name]
				if err != nil || !ok || ctx.Err() != nil || (value.ExpiresAt != nil && !value.ExpiresAt.After(time.Now())) {
					res.Error = "secret resolution unavailable"
				} else if err = s.redactor.Register(value.Value); err != nil {
					res.Error = "execution access ended"
				} else {
					res.Secret = &value
				}
			}
		}
	default:
		res.Error = "unknown execution request"
	}
	// Fence in-flight replies against clear, cancellation, and scope exit.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || ctx.Err() != nil || generation != s.generation {
		res = Response{Error: "execution access ended"}
	}
	_ = json.NewEncoder(w).Encode(res)
}

func Call(ctx context.Context, socket string, req Request) (Response, error) {
	var out Response
	if socket == "" {
		return out, errors.New("requires a live c2j execution")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second}
	raw, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://execution/resolve", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	response, err := client.Do(httpReq)
	if err != nil {
		return out, errors.New("execution resolver unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&out) != nil {
		return Response{}, errors.New("invalid execution resolver response")
	}
	if out.Error != "" {
		return Response{}, errors.New(out.Error)
	}
	return out, nil
}

type scopeKey struct{}

func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}
func FromContext(ctx context.Context) *Scope { s, _ := ctx.Value(scopeKey{}).(*Scope); return s }
func InheritedSelection(ctx context.Context) Selection {
	if s := FromContext(ctx); s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cleared {
			return Selection{Explicit: true}
		}
		return s.selection
	}
	return Selection{}
}

// Env strips inherited Git authentication/configuration before applying this scope.
func Env(ctx context.Context, base []string) []string {
	s := FromContext(ctx)
	if s == nil {
		return base
	}
	out := make([]string, 0, len(base)+14)
	for _, v := range base {
		k, _, _ := strings.Cut(v, "=")
		if strings.HasPrefix(k, "GIT_CONFIG") || strings.HasPrefix(k, "GIT_TRACE") || k == "GIT_ASKPASS" || k == "SSH_ASKPASS" || k == SocketEnv {
			continue
		}
		out = append(out, v)
	}
	out = append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=include.path", "GIT_CONFIG_VALUE_0="+filepath.Join(s.dir, "active.gitconfig"), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", SocketEnv+"="+filepath.Join(s.dir, "resolver.sock"))
	return out
}
func Command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = Env(ctx, os.Environ())
	return cmd
}

type secretLimitKey struct{}

// WithSecretLimit restricts trusted include preparation as well as later node
// execution. Nested includes can narrow but cannot restore a removed name.
func WithSecretLimit(ctx context.Context, names []string) context.Context {
	allowed := slices.Clone(names)
	if old, ok := ctx.Value(secretLimitKey{}).([]string); ok {
		allowed = nil
		for _, name := range names {
			if slices.Contains(old, name) {
				allowed = append(allowed, name)
			}
		}
	}
	return context.WithValue(ctx, secretLimitKey{}, allowed)
}

func CredentialNames(selection Selection, snapshot jobdb.ConfigurationSnapshot) ([]string, error) {
	name, err := selection.Resolve(snapshot)
	if err != nil || name == "" {
		return nil, err
	}
	p, err := Load(snapshot, name)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range p.Entries {
		if e.SecretRef != "" {
			names = append(names, e.SecretRef)
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}
