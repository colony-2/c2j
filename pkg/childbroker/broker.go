package childbroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

const (
	submitPath    = "/v1/child-jobs"
	sessionHeader = "X-C2J-Child-Job-Session"

	defaultClientTimeout = 30 * time.Second
)

type Submitter interface {
	SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error)
}

type Options struct {
	Current            jobcontext.Current
	Submitter          Submitter
	ContainerReachable bool
}

type Server struct {
	current   jobcontext.Current
	submitter Submitter

	endpoint  string
	token     string
	sessionID string
	host      string
	port      int

	server *http.Server
	once   sync.Once

	mu      sync.Mutex
	started []jobcontext.StartedJobContext
}

type ArtifactPayload struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

type EmbeddedRecipePayload struct {
	Name string `json:"name,omitempty"`
	YAML []byte `json:"yaml"`
}

type SubmitRequest struct {
	Start           workflowctl.StartJob    `json:"start"`
	Artifacts       []ArtifactPayload       `json:"artifacts,omitempty"`
	EmbeddedRecipes []EmbeddedRecipePayload `json:"embedded_recipes,omitempty"`
}

type SubmitResponse struct {
	TenantID string `json:"tenant_id"`
	JobID    string `json:"job_id"`
	Recipe   string `json:"recipe"`
}

func Start(ctx context.Context, opts Options) (*Server, error) {
	if opts.Submitter == nil {
		return nil, fmt.Errorf("child job broker submitter is required")
	}
	if !opts.Current.HasJob() {
		return nil, fmt.Errorf("child job broker current job context is required")
	}

	token, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	sessionID, err := randomHex(16)
	if err != nil {
		return nil, err
	}

	listener, advertiseHost, err := listen(opts.ContainerReachable)
	if err != nil {
		return nil, err
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("child job broker listener address has unexpected type %T", listener.Addr())
	}

	broker := &Server{
		current:   opts.Current,
		submitter: opts.Submitter,
		endpoint:  fmt.Sprintf("http://%s:%d%s", advertiseHost, tcpAddr.Port, submitPath),
		token:     token,
		sessionID: sessionID,
		host:      advertiseHost,
		port:      tcpAddr.Port,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(submitPath, broker.handleSubmit)
	broker.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := broker.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Default().Warn("child job broker stopped unexpectedly", "error", err)
		}
	}()
	if ctx != nil {
		go func() {
			<-ctx.Done()
			_ = broker.Close()
		}()
	}
	return broker, nil
}

func (s *Server) Close() error {
	var err error
	s.once.Do(func() {
		if s.server == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = s.server.Shutdown(ctx)
	})
	return err
}

func (s *Server) Env() map[string]string {
	if s == nil {
		return nil
	}
	return jobcontext.EnvForChildJobBroker(jobcontext.ChildJobBroker{
		Endpoint:  s.endpoint,
		Token:     s.token,
		SessionID: s.sessionID,
	})
}

func (s *Server) Port() int {
	if s == nil {
		return 0
	}
	return s.port
}

func (s *Server) Host() string {
	if s == nil {
		return ""
	}
	return s.host
}

func (s *Server) StartedJobs() jobcontext.StartedJobsContext {
	if s == nil {
		return jobcontext.StartedJobsContext{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := jobcontext.StartedJobsContext{
		JobIDs: make([]string, 0, len(s.started)),
		Items:  make([]jobcontext.StartedJobContext, 0, len(s.started)),
	}
	for _, item := range s.started {
		if strings.TrimSpace(item.JobID) == "" {
			continue
		}
		out.JobIDs = append(out.JobIDs, item.JobID)
		out.Items = append(out.Items, item)
	}
	return out
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req SubmitRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<20))
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode request: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.submit(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) authorized(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.Header.Get(sessionHeader) != s.sessionID {
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == s.token
}

func (s *Server) submit(ctx context.Context, req SubmitRequest) (SubmitResponse, error) {
	start := req.Start
	if strings.TrimSpace(start.TenantId) != strings.TrimSpace(s.current.TenantID) {
		return SubmitResponse{}, fmt.Errorf("child job submission must use the current tenant %q; got %q", s.current.TenantID, start.TenantId)
	}
	parent := jobcontext.ParentFromCurrent(s.current)
	start.Parent = &parent

	artifacts := make([]jobdb.Artifact, 0, len(req.Artifacts))
	for _, payload := range req.Artifacts {
		name := strings.TrimSpace(payload.Name)
		if name == "" {
			return SubmitResponse{}, fmt.Errorf("artifact name is required")
		}
		artifacts = append(artifacts, jobdb.NewArtifactFromBytes(name, append([]byte(nil), payload.Data...)))
	}
	start.Artifacts = artifacts

	recipes := make([]recipe.Recipe, 0, len(req.EmbeddedRecipes))
	for _, payload := range req.EmbeddedRecipes {
		if len(payload.YAML) == 0 {
			return SubmitResponse{}, fmt.Errorf("embedded recipe %q is empty", payload.Name)
		}
		rec, err := recipe.LoadRecipeFromReader(bytes.NewReader(payload.YAML))
		if err != nil {
			return SubmitResponse{}, fmt.Errorf("decode embedded recipe %q: %w", payload.Name, err)
		}
		recipes = append(recipes, *rec)
	}

	key, err := starter.StartRecipeJob(ctx, start, s.submitter, recipes...)
	if err != nil {
		return SubmitResponse{}, fmt.Errorf("submit child job: %w", err)
	}

	s.record(key, start)
	return SubmitResponse{
		TenantID: key.TenantId,
		JobID:    key.JobId,
		Recipe:   start.RecipeName,
	}, nil
}

func (s *Server) record(key jobdb.JobKey, start workflowctl.StartJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started = append(s.started, jobcontext.StartedJobContext{
		TenantID:             key.TenantId,
		JobID:                key.JobId,
		RecipeName:           start.RecipeName,
		ParentInvocationHash: s.current.InvocationHash,
	})
}

func Submit(ctx context.Context, broker jobcontext.ChildJobBroker, req SubmitRequest) (SubmitResponse, error) {
	endpoint := strings.TrimSpace(broker.Endpoint)
	if endpoint == "" {
		return SubmitResponse{}, fmt.Errorf("child job broker endpoint is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return SubmitResponse{}, fmt.Errorf("parse child job broker endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return SubmitResponse{}, fmt.Errorf("child job broker endpoint must be http or https")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return SubmitResponse{}, fmt.Errorf("encode child job request: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return SubmitResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+broker.Token)
	httpReq.Header.Set(sessionHeader, broker.SessionID)

	client := &http.Client{Timeout: defaultClientTimeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return SubmitResponse{}, fmt.Errorf("call child job broker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			msg = resp.Status
		}
		return SubmitResponse{}, fmt.Errorf("child job broker rejected submit: %s", msg)
	}
	var out SubmitResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return SubmitResponse{}, fmt.Errorf("decode child job broker response: %w", err)
	}
	return out, nil
}

func NewSubmitRequest(ctx context.Context, start workflowctl.StartJob, artifacts []jobdb.Artifact, recipes ...recipe.Recipe) (SubmitRequest, error) {
	req := SubmitRequest{
		Start: start,
	}
	req.Start.Artifacts = nil
	for _, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		data, err := artifact.Bytes(ctx)
		if err != nil {
			return SubmitRequest{}, fmt.Errorf("read artifact %q: %w", artifact.Name(), err)
		}
		req.Artifacts = append(req.Artifacts, ArtifactPayload{
			Name: artifact.Name(),
			Data: data,
		})
	}
	for _, rec := range recipes {
		raw, err := yaml.Marshal(&rec)
		if err != nil {
			return SubmitRequest{}, fmt.Errorf("encode embedded recipe %q: %w", rec.GetMetdata().ID, err)
		}
		req.EmbeddedRecipes = append(req.EmbeddedRecipes, EmbeddedRecipePayload{
			Name: rec.GetMetdata().ID,
			YAML: raw,
		})
	}
	return req, nil
}

func listen(containerReachable bool) (net.Listener, string, error) {
	if !containerReachable {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		return listener, "127.0.0.1", err
	}
	bindAddress, advertiseHost := containerListenAddress()
	listener, err := net.Listen("tcp", bindAddress)
	if err != nil && bindAddress != "0.0.0.0:0" {
		listener, err = net.Listen("tcp", "0.0.0.0:0")
	}
	return listener, advertiseHost, err
}

func containerListenAddress() (string, string) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return "127.0.0.1:0", "host.docker.internal"
	}
	if runningInContainer() {
		if ip := defaultRouteIPv4(); ip != "" {
			return "0.0.0.0:0", ip
		}
		if ip := interfaceIPv4("eth0"); ip != "" {
			return "0.0.0.0:0", ip
		}
	}
	if ip := interfaceIPv4("docker0"); ip != "" {
		return ip + ":0", "host.docker.internal"
	}
	return "0.0.0.0:0", "host.docker.internal"
}

func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	data, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	text := string(data)
	return strings.Contains(text, "docker") ||
		strings.Contains(text, "kubepods") ||
		strings.Contains(text, "containerd")
}

func defaultRouteIPv4() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 100*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return ""
	}
	if ipv4 := addr.IP.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ""
}

func interfaceIPv4(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip == nil {
			continue
		}
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String()
		}
	}
	return ""
}

func randomHex(bytesLen int) (string, error) {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate child job broker token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
