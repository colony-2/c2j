package childbroker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Prepare resolves an ordinary child operation without publication. Persist
// the complete descriptor before calling SubmitPrepared to recover uncertainty.
func Prepare(ctx context.Context, broker jobcontext.ChildJobBroker, req SubmitRequest) (starter.Descriptor, error) {
	if req.Start.GoalID != "" || req.Descriptor != nil {
		return starter.Descriptor{}, jobdb.ErrPrimaryGoalSelectionUnsupported
	}
	broker.Endpoint = strings.TrimRight(broker.Endpoint, "/") + "/prepare"
	return prepareRPC(ctx, broker, req)
}

func (s *Server) handlePrepare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req SubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<20)).Decode(&req); err != nil {
		writeSelectionError(w, err)
		return
	}
	if req.Start.GoalID != "" || req.Descriptor != nil {
		writeSelectionError(w, jobdb.ErrPrimaryGoalSelectionUnsupported)
		return
	}
	var descriptor starter.Descriptor
	req.prepare = &descriptor
	if _, err := s.submit(r.Context(), req); err != nil {
		writeSelectionError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(descriptor)
}
