package childbroker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/workflowctl"
)

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	api := jobrestart.FromContext(s.executionContext)
	if api == nil {
		http.Error(w, "restart history/replay/submission capability unavailable; upgrade the worker and CLI together", 501)
		return
	}
	var q jobrestart.Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&q); err != nil {
		http.Error(w, "invalid restart request: "+err.Error(), 400)
		return
	}
	if d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid restart request", 400)
		return
	}
	if err := jobrestart.ValidatePatch(q.Patch); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithCancel(s.executionContext)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	if access := permissions.FromContext(s.executionContext); access != nil {
		stop := context.AfterFunc(access.Context(), cancel)
		defer stop()
	}
	result, err := api.Restart(ctx, q)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if q.Operation == "submit" && result != nil && result.Ref != nil {
		if err := result.Ref.Validate(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.record(*result.Ref, workflowctl.StartJob{})
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
func (c JobClient) Restart(ctx context.Context, q jobrestart.Request) (*jobrestart.Response, error) {
	if err := jobrestart.ValidatePatch(q.Patch); err != nil {
		return nil, err
	}
	resp, err := c.callPayload(ctx, q, "/restart", false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out jobrestart.Response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
