package childbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

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
