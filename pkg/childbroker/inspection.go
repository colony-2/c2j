package childbroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinputs"
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
	ctx, cancel := context.WithCancelCause(s.executionContext)
	defer cancel(nil)
	stop := context.AfterFunc(r.Context(), func() { cancel(context.Cause(r.Context())) })
	defer stop()
	if access := permissions.FromContext(s.executionContext); access != nil {
		scope := access.Context()
		stopScope := context.AfterFunc(scope, func() { cancel(context.Cause(scope)) })
		defer stopScope()
		if cause := context.Cause(scope); cause != nil {
			cancel(cause)
		}
	}
	// AfterFunc callbacks run asynchronously. Reject lifetimes already ended
	// at dispatch even when the API does not itself observe cancellation.
	if cause := context.Cause(r.Context()); cause != nil {
		cancel(cause)
	}
	w.Header().Set("Cache-Control", "no-store")
	err := context.Cause(ctx)
	if strings.HasSuffix(r.URL.Path, "/download") {
		var reader io.ReadCloser
		if err == nil {
			reader, err = api.Download(ctx, q)
		}
		if err != nil {
			// Named downloads retain their existing diagnostic contract. Typed
			// saved-diff errors belong to the authenticated exact-reference route.
			if !downloadRequest(q) {
				http.Error(w, err.Error(), 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			diagnostic := jobinspect.WorkspaceDiffDiagnostic("storage", "artifact download failed", err)
			status, _, typed := jobdb.CellErrorStatus(err)
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, jobdb.ErrChapterNotFound) || typed && status == 404 {
				diagnostic = &jobinspect.WorkspaceDiffError{Version: 1, Code: "missing", Detail: "saved artifact is no longer retained", Cause: err}
			}
			_ = json.NewEncoder(w).Encode(diagnostic)
			return
		}
		var size *int64
		if q.Reference != nil && q.Reference.SizeBytes >= 0 {
			n := q.Reference.SizeBytes
			size = &n
		}
		reader = jobinspect.NewWorkspaceDiffStream(ctx, reader, size)
		defer reader.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-C2J-Download-Version", "1")
		w.Header().Set("Trailer", "X-C2J-Download-Error, X-C2J-Download-Complete")
		_, err = io.Copy(w, reader)
		err = errors.Join(err, reader.Close())
		if err != nil {
			raw, _ := json.Marshal(jobinspect.WorkspaceDiffDiagnostic("read", "artifact stream incomplete", err))
			w.Header().Set("X-C2J-Download-Error", base64.RawURLEncoding.EncodeToString(raw))
		} else {
			w.Header().Set("X-C2J-Download-Complete", "1")
		}

		return
	}
	var result *jobinspect.Response
	if err == nil {
		result, err = api.Inspect(ctx, q)
	}
	if err != nil {
		if q.Operation == "workspace_diff" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(jobinspect.WorkspaceDiffDiagnostic("storage", "saved diff inspection failed", err))
			return
		}
		if q.Operation == "metrics" && result != nil && result.Metrics != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(result)
			return
		}
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
			return nil, context.Cause(ctx)
		}
		return nil, fmt.Errorf("task inspection helper unavailable")
	}
	resp.Body = &transportBody{ReadCloser: resp.Body, close: transport.CloseIdleConnections}
	if request, ok := q.(jobinspect.Request); ok && request.Operation == "metrics" && resp.StatusCode == http.StatusUnprocessableEntity {
		return resp, nil
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if _, ok := q.(jobinputs.Request); ok {
			if readErr != nil {
				return nil, jobinputs.Diagnostic("transport", "cannot read history diagnostic", readErr)
			}
			if d, e := jobinputs.DecodeError(b); e == nil {
				return nil, d
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return nil, jobinputs.Diagnostic("denied", "helper authentication denied", jobdb.ErrLeasePermissionDenied)
			}
			return nil, jobinputs.Diagnostic("unsupported", "history helper unavailable; upgrade worker and CLI together", nil)
		}
		if request, ok := q.(jobinspect.Request); ok && (request.Operation == "workspace_diff" || downloadRequest(request)) {
			if readErr != nil {
				return nil, jobinspect.WorkspaceDiffDiagnostic("transport", "cannot read helper diagnostic", readErr)
			}
			var diagnostic jobinspect.WorkspaceDiffError
			if json.Unmarshal(b, &diagnostic) == nil && diagnostic.Version == 1 && diagnostic.Code != "" && diagnostic.Detail != "" {
				switch diagnostic.Code {
				case "canceled":
					diagnostic.Cause = context.Canceled
				case "timeout":
					diagnostic.Cause = context.DeadlineExceeded
				case "lease_lost":
					diagnostic.Cause = jobdb.ErrExecutionLeaseLost
				case "denied":
					diagnostic.Cause = jobdb.ErrLeasePermissionDenied
				}
				return nil, &diagnostic
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return nil, jobinspect.WorkspaceDiffDiagnostic("denied", "helper authorization denied", jobdb.ErrLeasePermissionDenied)
			}
			return nil, jobinspect.WorkspaceDiffDiagnostic("unsupported", "saved diff helper unsupported; upgrade worker and CLI together", nil)
		}
		if isChangesPayload(q) {
			if readErr != nil {
				return nil, changesMetadataReadError(ctx, readErr)
			}
			var diagnostic jobinspect.ChangesError
			if json.Unmarshal(b, &diagnostic) == nil && (diagnostic.Version != 0 || diagnostic.Code != "") {
				if err := jobinspect.ValidateChangesDiagnostic(&diagnostic); err != nil {
					return nil, err
				}
				return nil, &diagnostic
			}
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return nil, &jobinspect.ChangesError{Version: 1, Code: "job_denied", Detail: "Changes helper authorization failed"}
			}
			return nil, &jobinspect.ChangesError{Version: jobinspect.ChangesVersion, Code: "protocol", Detail: "unsupported changes helper response; upgrade worker and CLI together"}
		}
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
		if request, ok := q.(jobinspect.Request); ok && request.Operation == "metrics" && (resp.StatusCode == 501 || resp.StatusCode == 404 || (resp.StatusCode == 400 && (strings.TrimSpace(string(b)) == "invalid inspection request" || strings.Contains(string(b), "unknown inspection operation")))) {
			return nil, fmt.Errorf("metrics inspection unavailable; upgrade the worker and CLI together")
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
	decoder := json.NewDecoder(resp.Body)
	if err = decoder.Decode(&out); err != nil {
		if q.Operation == "workspace_diff" {
			return nil, jobinspect.WorkspaceDiffDiagnostic("protocol", "invalid saved-diff inspection envelope", err)
		}
		return nil, err
	}
	if q.Operation == "workspace_diff" {
		var trailing any
		if err = decoder.Decode(&trailing); err != io.EOF {
			return nil, jobinspect.WorkspaceDiffDiagnostic("protocol", "saved-diff inspection requires exactly one JSON response", err)
		}
	}
	if out.Version != jobinspect.Version {
		if q.Operation == "workspace_diff" {
			return nil, jobinspect.WorkspaceDiffDiagnostic("protocol", "unsupported saved-diff inspection envelope version; upgrade worker and CLI together", nil)
		}
		if q.Family != nil {
			return nil, fmt.Errorf("%w; upgrade the worker and CLI together", jobinspect.ErrFamilyProtocol)
		}
		return nil, fmt.Errorf("unsupported job inspection response version")
	}
	if q.Operation == "metrics" {
		if out.Metrics == nil || out.Metrics.Snapshot.Version != 1 || out.Metrics.Snapshot.ReducerVersion != 1 {
			return nil, fmt.Errorf("metrics inspection unavailable; upgrade the worker and CLI together")
		}
		if resp.StatusCode != http.StatusOK || out.Metrics.Snapshot.Coverage.History != "complete" {
			return &out, fmt.Errorf("metric history %s", out.Metrics.Snapshot.Coverage.History)
		}
	}
	if q.Family != nil {
		if err := jobinspect.ValidateFamilyResponse(&out); err != nil {
			return nil, err
		}
	}
	if q.Operation == "workspace_diff" {
		options := jobinspect.WorkspaceDiffOptions{}
		if q.WorkspaceDiff != nil {
			options = *q.WorkspaceDiff
		}
		if err := jobinspect.ValidateWorkspaceDiff(jobinspect.WorkspaceDiffRequest{JobID: q.JobID, WorkspaceDiffOptions: options}, out.WorkspaceDiff); err != nil {
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

func downloadRequest(q jobinspect.Request) bool {
	return q.Operation == "artifact" && q.Reference != nil
}

var _ jobinspect.WorkspaceDiffAPI = JobClient{}

func (c JobClient) OpenWorkspaceDiff(ctx context.Context, q jobinspect.WorkspaceDiffRequest) (*jobinspect.WorkspaceDiff, io.ReadCloser, error) {
	if err := q.Validate(); err != nil {
		return nil, nil, err
	}
	response, err := c.Inspect(ctx, jobinspect.Request{Operation: "workspace_diff", JobID: q.JobID, WorkspaceDiff: &q.WorkspaceDiffOptions})
	if err != nil {
		return nil, nil, jobinspect.WorkspaceDiffDiagnostic("protocol", "saved diff metadata failed", err)
	}
	m := response.WorkspaceDiff
	if m.Status == "unavailable" {
		return m, nil, nil
	}
	if m.Source == nil {
		return m, io.NopCloser(bytes.NewReader(nil)), nil
	}
	resp, err := c.call(ctx, jobinspect.Request{Operation: "artifact", JobID: m.Source.Producer.JobID, Reference: &m.Source.Key}, true)
	if err != nil {
		var diagnostic *jobinspect.WorkspaceDiffError
		if errors.As(err, &diagnostic) && diagnostic.Code == "missing" {
			m.Status = "unavailable"
			m.Source = nil
			m.Bytes = nil
			m.Reasons = []string{"saved artifact is no longer retained"}
			return m, nil, nil
		}
		return nil, nil, jobinspect.WorkspaceDiffDiagnostic("transport", "saved diff download failed", err)
	}
	if resp.Header.Get("X-C2J-Download-Version") != "1" {
		_ = resp.Body.Close()
		return nil, nil, jobinspect.WorkspaceDiffDiagnostic("unsupported", "saved diff completion unsupported; upgrade worker and CLI together", nil)
	}
	body := &workspaceDownloadBody{ReadCloser: resp.Body, response: resp}
	return m, jobinspect.NewWorkspaceDiffStream(ctx, body, m.Bytes), nil
}

type workspaceDownloadBody struct {
	io.ReadCloser
	response *http.Response
}

func (b *workspaceDownloadBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		if encoded := b.response.Trailer.Get("X-C2J-Download-Error"); encoded != "" {
			raw, e := base64.RawURLEncoding.DecodeString(encoded)
			var diagnostic jobinspect.WorkspaceDiffError
			if e != nil || json.Unmarshal(raw, &diagnostic) != nil || diagnostic.Version != 1 || diagnostic.Code == "" || diagnostic.Detail == "" {
				return n, jobinspect.WorkspaceDiffDiagnostic("protocol", "invalid artifact completion diagnostic", e)
			}
			switch diagnostic.Code {
			case "canceled":
				diagnostic.Cause = context.Canceled
			case "timeout":
				diagnostic.Cause = context.DeadlineExceeded
			case "lease_lost":
				diagnostic.Cause = jobdb.ErrExecutionLeaseLost
			}
			return n, &diagnostic
		}
		if b.response.Trailer.Get("X-C2J-Download-Complete") != "1" {
			return n, jobinspect.WorkspaceDiffDiagnostic("protocol", "missing artifact completion", nil)
		}
	}
	return n, err
}
