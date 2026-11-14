package childbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func (s *Server) handleJobInspection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	api := jobinspect.FromContext(s.executionContext)
	if api == nil {
		http.Error(w, "job inspection unavailable", 501)
		return
	}
	var q jobinspect.Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid inspection request", 400)
		return
	}
	ctx, cancel := context.WithCancel(s.executionContext)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	if access := permissions.FromContext(s.executionContext); access != nil {
		stopScope := context.AfterFunc(access.Context(), cancel)
		defer stopScope()
	}
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasSuffix(r.URL.Path, "/download") {
		reader, err := api.Download(ctx, q)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Trailer", "X-C2J-Download-Error")
		if _, err = io.Copy(w, reader); err != nil {
			w.Header().Set("X-C2J-Download-Error", "artifact stream incomplete")
		}
		return
	}
	result, err := api.Inspect(ctx, q)
	if err != nil {
		if q.Operation == "events" || q.Family != nil {
			if q.Family != nil && (errors.Is(err, jobdb.ErrExecutionLeaseLost) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(&jobdb.CellError{Code: jobinspect.FamilyErrorReason(err), Status: 409})
				return
			}
			if errors.Is(err, jobdb.ErrLeasePermissionDenied) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(403)
				_ = json.NewEncoder(w).Encode(&jobdb.CellError{Code: "lease_permission_denied", Status: 403})
				return
			}
			if status, code, ok := jobdb.CellErrorStatus(err); ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(&jobdb.CellError{Code: code, Status: status})
				return
			}
		}
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// JobClient is the task-scoped transport. It never opens ambient JobDB credentials.
type JobClient struct{ Broker jobcontext.ChildJobBroker }

func (c JobClient) call(ctx context.Context, q jobinspect.Request, download bool) (*http.Response, error) {
	suffix := "/inspect"
	if download {
		suffix += "/download"
	}
	return c.callPayload(ctx, q, suffix, q.Operation == "events" || q.Family != nil)
}
func (c JobClient) callPayload(ctx context.Context, q any, suffix string, typedErrors bool) (*http.Response, error) {
	u, err := url.Parse(strings.TrimRight(c.Broker.Endpoint, "/") + suffix)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid task inspection endpoint")
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Broker.Token)
	req.Header.Set(sessionHeader, c.Broker.SessionID)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		transport.CloseIdleConnections()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("task inspection helper unavailable")
	}
	resp.Body = &transportBody{ReadCloser: resp.Body, close: transport.CloseIdleConnections}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if typedErrors {
			var typed jobdb.CellError
			if json.Unmarshal(b, &typed) == nil && typed.Code != "" && typed.Status == resp.StatusCode {
				if typed.Code == "lease_permission_denied" {
					return nil, jobdb.ErrLeasePermissionDenied
				}
				if family, ok := q.(jobinspect.Request); ok && family.Family != nil {
					switch typed.Code {
					case "lease_lost":
						return nil, jobdb.ErrExecutionLeaseLost
					case "canceled":
						return nil, context.Canceled
					case "timeout":
						return nil, context.DeadlineExceeded
					case "invalid_request", "unsupported_operation":
						return nil, fmt.Errorf("%w (%s); upgrade the worker and CLI together", jobinspect.ErrFamilyProtocol, typed.Code)
					}
				}
				return nil, &typed
			}
		}
		if family, ok := q.(jobinspect.Request); ok && family.Family != nil && (resp.StatusCode == 501 || resp.StatusCode == 404 || (resp.StatusCode == 400 && strings.TrimSpace(string(b)) == "invalid inspection request")) {
			return nil, fmt.Errorf("%w; upgrade the worker and CLI together", jobinspect.ErrFamilyProtocol)
		}
		return nil, fmt.Errorf("task inspection rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

type transportBody struct {
	io.ReadCloser
	close func()
}

func (b *transportBody) Close() error { err := b.ReadCloser.Close(); b.close(); return err }
func (c JobClient) Inspect(ctx context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	if q.Family != nil {
		if err := jobinspect.ValidateFamilyRequest(q); err != nil {
			return nil, err
		}
	}
	resp, err := c.call(ctx, q, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out jobinspect.Response
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Version != jobinspect.Version {
		if q.Family != nil {
			return nil, fmt.Errorf("%w; upgrade the worker and CLI together", jobinspect.ErrFamilyProtocol)
		}
		return nil, fmt.Errorf("unsupported job inspection response version")
	}
	if q.Family != nil {
		if err := jobinspect.ValidateFamilyResponse(&out); err != nil {
			return nil, err
		}
	}
	return &out, nil
}
func (c JobClient) Download(ctx context.Context, q jobinspect.Request) (io.ReadCloser, error) {
	resp, err := c.call(ctx, q, true)
	if err != nil {
		return nil, err
	}
	return &downloadBody{ReadCloser: resp.Body, response: resp}, nil
}

type downloadBody struct {
	io.ReadCloser
	response *http.Response
}

func (b *downloadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF && b.response.Trailer.Get("X-C2J-Download-Error") != "" {
		err = fmt.Errorf("artifact stream incomplete")
	}
	return n, err
}
