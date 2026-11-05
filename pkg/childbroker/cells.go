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
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func (s *Server) handleCreateCell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	access := permissions.FromContext(s.executionContext)
	if access == nil {
		http.Error(w, "restricted cell creation unavailable", http.StatusForbidden)
		return
	}
	var req jobdb.CreateCellRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid cell creation request", http.StatusBadRequest)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid cell creation request", http.StatusBadRequest)
		return
	}
	// The authenticated task owns the tenant; the subprocess cannot retarget it.
	req.TenantID = s.current.TenantID
	ctx, cancel := context.WithCancel(s.executionContext)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	cell, err := access.CreateCell(ctx, req)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, jobdb.ErrLeasePermissionDenied) {
			status = http.StatusForbidden
		} else if code, _, ok := jobdb.CellErrorStatus(err); ok {
			status = code
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(cell)
}

// CreateCell uses the live task's restricted authority, never an admin runtime.
func CreateCell(ctx context.Context, broker jobcontext.ChildJobBroker, request jobdb.CreateCellRequest) (jobdb.Cell, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return jobdb.Cell{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(broker.Endpoint, "/")+"/cells", bytes.NewReader(raw))
	if err != nil {
		return jobdb.Cell{}, err
	}
	req.Header.Set("Authorization", "Bearer "+broker.Token)
	req.Header.Set(sessionHeader, broker.SessionID)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: defaultClientTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return jobdb.Cell{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if resp.StatusCode == http.StatusForbidden {
			return jobdb.Cell{}, fmt.Errorf("cell creation rejected: %s: %w", strings.TrimSpace(string(message)), jobdb.ErrLeasePermissionDenied)
		}
		return jobdb.Cell{}, fmt.Errorf("cell creation rejected (%s): %s", resp.Status, strings.TrimSpace(string(message)))
	}
	var cell jobdb.Cell
	err = json.NewDecoder(resp.Body).Decode(&cell)
	return cell, err
}

func (s *Server) handleResolveCell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	scope, ok := jobref.FromContext(s.executionContext)
	if !ok || scope.ResolveCell == nil {
		http.Error(w, "cell resolution unavailable", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithCancel(s.executionContext)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	cell, err := scope.ResolveCell(ctx, r.URL.Query().Get("name"))
	if err != nil {
		status := http.StatusForbidden
		if code, _, ok := jobdb.CellErrorStatus(err); ok {
			status = code
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(cell)
}

func ResolveCell(ctx context.Context, broker jobcontext.ChildJobBroker, name string) (jobdb.ResolvedCell, error) {
	endpoint := strings.TrimRight(broker.Endpoint, "/") + "/cell?name=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return jobdb.ResolvedCell{}, err
	}
	req.Header.Set("Authorization", "Bearer "+broker.Token)
	req.Header.Set(sessionHeader, broker.SessionID)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: defaultClientTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return jobdb.ResolvedCell{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return jobdb.ResolvedCell{}, jobdb.ErrCellNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return jobdb.ResolvedCell{}, fmt.Errorf("cell resolution rejected: %s", resp.Status)
	}
	var cell jobdb.ResolvedCell
	err = json.NewDecoder(resp.Body).Decode(&cell)
	return cell, err
}
