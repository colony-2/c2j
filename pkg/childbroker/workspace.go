package childbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobcontext"
)

type WorkspaceInfo struct {
	Version int                           `json:"version"`
	Git     gitstate.GlobalGitTaskContext `json:"git"`
}

func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.workspace == nil {
		http.Error(w, "task-start workspace context is unavailable", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(WorkspaceInfo{Version: 1, Git: s.workspace.Git})
}

// Workspace verifies server support before a CLI sends a workspace submission.
// Older brokers ignore unknown JSON fields, so sending only a mode is insufficient.
func Workspace(ctx context.Context, broker jobcontext.ChildJobBroker) (WorkspaceInfo, error) {
	var info WorkspaceInfo
	endpoint, err := url.Parse(strings.TrimRight(broker.Endpoint, "/") + "/workspace")
	if err != nil {
		return info, err
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return info, fmt.Errorf("child job broker endpoint must be http or https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return info, err
	}
	req.Header.Set("Authorization", "Bearer "+broker.Token)
	req.Header.Set(sessionHeader, broker.SessionID)
	client := &http.Client{Timeout: defaultClientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return info, fmt.Errorf("read task workspace: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return info, fmt.Errorf("task workspace unavailable (HTTP %d); workspace submission requires a worker with workspace support", resp.StatusCode)
	}
	if err = json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return info, err
	}
	if info.Version != 1 || info.Git.BaseRepo == "" || info.Git.BaseRef == "" {
		return info, fmt.Errorf("unsupported or missing task workspace context")
	}
	return info, nil
}
