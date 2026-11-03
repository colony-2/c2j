package childbroker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/selfinfo"
)

// InspectionError contains only a stable safe reason, never a URL or response body.
type InspectionError struct{ Code string }

func (e *InspectionError) Error() string { return "execution inspection: " + e.Code }
func (s *Server) handleSelf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.self == nil {
		http.Error(w, "inspection unsupported", http.StatusNotFound)
		return
	}
	info := s.self()
	raw, err := selfinfo.MarshalSafe(info, secrets.RedactorFromContext(s.executionContext).Redact)
	if err != nil || len(raw) > selfinfo.MaxResponseBytes {
		http.Error(w, "inspection response unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}

// Inspect uses the advertised invocation transport only; it never falls back to
// ambient JobDB authority and never follows redirects with a broker credential.
func Inspect(ctx context.Context, broker jobcontext.ChildJobBroker) (selfinfo.Info, error) {
	fail := func(code string) (selfinfo.Info, error) { return selfinfo.Info{}, &InspectionError{Code: code} }
	endpoint, err := url.Parse(strings.TrimRight(broker.Endpoint, "/") + "/self")
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fail("invalid_helper_endpoint")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return fail("invalid_helper_endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+broker.Token)
	req.Header.Set(sessionHeader, broker.SessionID)
	// The authenticated local helper is not a general HTTP service. Avoid ambient
	// proxies, redirect handlers, and transports that could receive credentials.
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, TLSHandshakeTimeout: 2 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail("helper_timeout")
		}
		return fail("helper_unavailable")
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusNotImplemented:
		return fail("inspection_unsupported")
	case http.StatusUnauthorized, http.StatusForbidden:
		return fail("helper_authentication_failed")
	case http.StatusOK:
	default:
		return fail("helper_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, selfinfo.MaxResponseBytes+1))
	if err != nil {
		return fail("invalid_helper_response")
	}
	if len(raw) > selfinfo.MaxResponseBytes {
		return fail("helper_response_too_large")
	}
	var info selfinfo.Info
	if err = json.Unmarshal(raw, &info); err != nil || info.SchemaVersion != selfinfo.Version || info.Execution.Status != "present" || info.Execution.TenantID == nil || info.Execution.JobID == nil {
		return fail("invalid_helper_response")
	}
	return info, nil
}
