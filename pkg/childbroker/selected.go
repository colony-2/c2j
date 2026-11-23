package childbroker

import (
	"context"
	"encoding/json"
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
	return starter.Descriptor{}, jobdb.ErrPrimaryGoalSelectionUnsupported
}
func (s *Server) handlePrepareSelected(w http.ResponseWriter, r *http.Request) {
	writeSelectionError(w, jobdb.ErrPrimaryGoalSelectionUnsupported)
}
func (s *Server) submitDescriptor(ctx context.Context, d starter.Descriptor) (SubmitResponse, error) {
	job, err := d.Request()
	if err != nil {
		return SubmitResponse{}, err
	}
	if (job.GoalID != "" && d.Version != 1) || job.JobType != starter.RecipeJobType || job.TenantId != s.current.TenantID {
		return SubmitResponse{}, jobdb.ErrPrimaryGoalSelectionForbidden
	}
	var start workflowctl.StartJob
	if err := json.Unmarshal(d.Input, &start); err != nil {
		return SubmitResponse{}, err
	}
	expectedParent := jobcontext.ParentFromCurrent(s.current)
	parent := start.Parent
	if parent != nil {
		copyParent := *parent
		copyParent.GoalID = ""
		parent = &copyParent
	}
	if parent == nil || !reflect.DeepEqual(*parent, expectedParent) || !reflect.DeepEqual(start.JobContext.GitProfile, gitprofile.InheritedSelection(s.executionContext)) {
		return SubmitResponse{}, jobdb.ErrPrimaryGoalSelectionForbidden
	}
	var meta starter.JobMetadata
	if err := json.Unmarshal(job.Metadata, &meta); err != nil {
		return SubmitResponse{}, err
	}
	// The descriptor's first-class goal is authoritative. Historical input may
	// contain a stale duplicate; newly prepared input omits it entirely.
	if start.TenantId != job.TenantId || start.JobID != job.JobID || start.Cell != job.Cell || start.CellRepo != job.CellRepo || start.Parent == nil || start.Parent.JobID != s.current.JobID || start.Parent.TenantID != s.current.TenantID || meta.ParentJobID != s.current.JobID || meta.ParentInvocationHash != s.current.InvocationHash {
		return SubmitResponse{}, jobdb.ErrPrimaryGoalSelectionForbidden
	}
	ref, err := d.Submit(ctx, s.submitter)
	if err != nil {
		return SubmitResponse{}, err
	}
	s.record(ref, start)
	response := SubmitResponse{Cell: ref.Cell, CellID: ref.CellID, CellRepo: ref.CellRepo, TenantID: ref.TenantID, JobID: ref.JobID, Recipe: start.RecipeName}
	if d.Version == 1 && job.GoalID != "" {
		response.GoalID = ref.GoalID
	} // Frozen historical receipt, not current context.
	return response, nil
}

// SubmitPrepared publishes a retained ordinary or historical descriptor using
// the current invocation's original submission authority.
func SubmitPrepared(ctx context.Context, broker jobcontext.ChildJobBroker, d starter.Descriptor) (SubmitResponse, error) {
	job, err := d.Request()
	if err != nil {
		return SubmitResponse{}, err
	}
	return Submit(ctx, broker, SubmitRequest{Descriptor: &d, Start: workflowctl.StartJob{TenantId: job.TenantId, JobID: job.JobID, GoalID: job.GoalID}})
}

func SubmitSelected(ctx context.Context, broker jobcontext.ChildJobBroker, d starter.Descriptor) (SubmitResponse, error) {
	job, err := d.Request()
	if err != nil {
		return SubmitResponse{}, err
	}
	if d.Version != 1 || job.GoalID == "" {
		return SubmitResponse{}, jobdb.ErrInvalidDependency
	}
	return Submit(ctx, broker, SubmitRequest{Descriptor: &d, Start: workflowctl.StartJob{TenantId: job.TenantId, JobID: job.JobID, GoalID: job.GoalID}})
}
