package childbroker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/pkg/gitprofile"
	"net/http"
	"reflect"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// PrepareSelected resolves workspace bytes and recipes without publishing.
// The caller must persist the returned descriptor before SubmitSelected.
func PrepareSelected(ctx context.Context, broker jobcontext.ChildJobBroker, req SubmitRequest) (starter.Descriptor, error) {
	if req.Start.GoalID == "" {
		return starter.Descriptor{}, fmt.Errorf("primary goal is required")
	}
	broker.Endpoint += "/primary-goal/prepare"
	// Use the ordinary RPC codec at this dedicated path: its response holds a
	// descriptor in a distinct envelope, never an accepted job identity.
	return prepareSelectedRPC(ctx, broker, req)
}
func (s *Server) handlePrepareSelected(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if !s.authorized(r) {
		w.WriteHeader(401)
		return
	}
	var req SubmitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<20)).Decode(&req); err != nil {
		writeSelectionError(w, err)
		return
	}
	if req.Start.GoalID == "" || req.Descriptor != nil {
		writeSelectionError(w, jobdb.ErrInvalidDependency)
		return
	}
	var d starter.Descriptor
	req.prepare = &d
	if _, err := s.submit(r.Context(), req); err != nil {
		writeSelectionError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}
func (s *Server) submitDescriptor(ctx context.Context, d starter.Descriptor) (SubmitResponse, error) {
	job, err := d.Request()
	if err != nil {
		return SubmitResponse{}, err
	}
	if job.GoalID == "" || job.JobType != starter.RecipeJobType || job.TenantId != s.current.TenantID {
		return SubmitResponse{}, jobdb.ErrPrimaryGoalSelectionForbidden
	}
	var start workflowctl.StartJob
	if err := json.Unmarshal(d.Input, &start); err != nil {
		return SubmitResponse{}, err
	}
	expectedParent := jobcontext.ParentFromCurrent(s.current)
	if start.Parent == nil || !reflect.DeepEqual(*start.Parent, expectedParent) || !reflect.DeepEqual(start.JobContext.GitProfile, gitprofile.InheritedSelection(s.executionContext)) {
		return SubmitResponse{}, jobdb.ErrPrimaryGoalSelectionForbidden
	}
	var meta starter.JobMetadata
	if err := json.Unmarshal(job.Metadata, &meta); err != nil {
		return SubmitResponse{}, err
	}
	if start.TenantId != job.TenantId || start.JobID != job.JobID || start.GoalID != job.GoalID || start.Cell != job.Cell || start.CellRepo != job.CellRepo || start.Parent == nil || start.Parent.JobID != s.current.JobID || start.Parent.TenantID != s.current.TenantID || meta.ParentJobID != s.current.JobID || meta.ParentInvocationHash != s.current.InvocationHash {
		return SubmitResponse{}, jobdb.ErrPrimaryGoalSelectionForbidden
	}
	ref, err := d.Submit(ctx, s.submitter)
	if err != nil {
		return SubmitResponse{}, err
	}
	s.record(ref, start)
	return SubmitResponse{Cell: ref.Cell, CellID: ref.CellID, CellRepo: ref.CellRepo, TenantID: ref.TenantID, JobID: ref.JobID, GoalID: ref.GoalID, Recipe: start.RecipeName}, nil
}
func SubmitSelected(ctx context.Context, broker jobcontext.ChildJobBroker, d starter.Descriptor) (SubmitResponse, error) {
	job, err := d.Request()
	if err != nil {
		return SubmitResponse{}, err
	}
	if job.GoalID == "" {
		return SubmitResponse{}, jobdb.ErrInvalidDependency
	}
	return Submit(ctx, broker, SubmitRequest{Descriptor: &d, Start: workflowctl.StartJob{TenantId: job.TenantId, JobID: job.JobID, GoalID: job.GoalID}})
}
