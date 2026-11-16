package jobinspect

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type changesCandidate struct {
	w            Workspace
	a            *attempt
	art          *Artifact
	order        int
	lineageScans int
}

func changesWorkspace(a *attempt, edge string) Workspace {
	g := a.in.Git
	w := Workspace{WorkspaceState: coretask.WorkspaceState{Repository: g.BaseRepo, Cell: g.GetWorkspaceCellName(), BaseRef: g.BaseRef, HeadCommit: g.PersistHash}, Task: a.task.ID, Attempt: a.task.Attempt, Boundary: edge}
	w.BaseCommit = g.ParentHash
	if w.BaseCommit == "" {
		w.BaseCommit = g.ResolvedBaseHash
	}
	if g.Workspace != nil {
		w.ScopeID = g.Workspace.ScopeID
		w.EmptyBase = g.Workspace.HasEmptyBase(g.ResolvedBaseHash, g.ParentHash)
	}
	if edge == "before" {
		if a.activity.WorkspaceBefore != nil {
			w.WorkspaceState = *a.activity.WorkspaceBefore
		}
	} else {
		if a.activity.WorkspaceAfter != nil {
			w.WorkspaceState = *a.activity.WorkspaceAfter
		} else if a.activity.Git.PersistHash != "" {
			w.HeadCommit = a.activity.Git.PersistHash
			if a.activity.Git.ParentHash != "" {
				w.BaseCommit = a.activity.Git.ParentHash
			}
		} else {
			w.HeadCommit = ""
			w.EmptyBase = false
		}
	}
	if w.ScopeID == "" {
		w.ScopeID = a.activity.Scope
	}
	if edge == "after" && a.activity.Git.ParentHash != "" {
		w.BaseCommit = a.activity.Git.ParentHash
	} else if g.ParentHash != "" {
		w.BaseCommit = g.ParentHash
	}
	if w.BaseCommit != "" {
		w.EmptyBase = false
	}
	return w
}

func sameChangesRepository(a, b string) bool {
	if a == b {
		return true
	}
	x, e := jobdb.NormalizeCellRepo(a)
	if e != nil {
		return false
	}
	y, e := jobdb.NormalizeCellRepo(b)
	return e == nil && x == y
}

// mainWorkspace uses the submitted root context. For legacy unscoped jobs the
// owning repository plus an unscoped invocation is root evidence. A scoped
// workspace cannot become main merely by sharing that repository or cell.
func mainWorkspace(h *history, ownerRepository string) (string, string, error) {
	var input struct {
		Context contextual.JobContext `json:"context"`
	}
	if h.run.Start.Input == nil || json.Unmarshal(h.run.Start.Input.Data, &input) != nil {
		return "", "", changesErr("workspace_unavailable", "submitted root context is unavailable")
	}
	repo, scope := input.Context.GitBase.BaseRepo, ""
	if input.Context.Workspace != nil {
		scope = input.Context.Workspace.ScopeID
		if scope == "" {
			return "", "", changesErr("workspace_ambiguous", "root workspace has no scope identity")
		}
	}
	if repo == "" && input.Context.Workspace == nil {
		repo = ownerRepository
	}
	if repo == "" {
		return "", "", changesErr("workspace_unavailable", "submitted root repository is unavailable")
	}
	return repo, scope, nil
}

func (c *ChangesService) selectLatest(ctx context.Context, q ChangesRequest) (*history, changesCandidate, []changesCandidate, error) {
	h, err := c.service.load(ctx, q.JobID)
	if err != nil {
		return nil, changesCandidate{}, nil, ChangesDiagnostic(err)
	}
	changesWaitingInputs(h)
	owner, err := c.service.Runtime.GetJob(ctx, h.run.Job.JobKey)
	if err != nil {
		return nil, changesCandidate{}, nil, ChangesDiagnostic(err)
	}
	repo, scope, err := mainWorkspace(h, owner.CellRepo)
	if err != nil {
		return nil, changesCandidate{}, nil, err
	}
	var candidates []changesCandidate
	for _, as := range h.attempts {
		for _, a := range as {
			// Scope comes from invocation identity, not a possibly misleading result.
			invScope := ""
			if a.in.Git.Workspace != nil {
				invScope = a.in.Git.Workspace.ScopeID
			}
			if !sameChangesRepository(a.in.Git.BaseRepo, repo) || invScope != scope {
				continue
			}
			edge := "after"
			if a.task.OutputAvailability == "pending" {
				edge = "before"
			}
			recorded := a.activity.WorkspaceAfter
			if edge == "before" {
				recorded = a.activity.WorkspaceBefore
			}
			if recorded != nil && recorded.EmptyBase && recorded.BaseCommit != "" {
				return nil, changesCandidate{}, nil, changesErr("invalid_objects", "recorded empty base contradicts its commit hash")
			}
			w := changesWorkspace(a, edge)
			if !sameChangesRepository(w.Repository, repo) || w.ScopeID != scope {
				return nil, changesCandidate{}, nil, changesErr("source_mismatch", "recorded workspace contradicts invocation identity")
			}
			v := changesCandidate{w: w, a: a, order: a.order}
			arts := a.task.Artifacts
			if edge == "before" {
				arts = a.inputArtifacts
			}
			for _, art := range arts {
				if art.Name == gitstate.ThinPackArtifactName && art.Reference.Stored != nil {
					if v.art != nil && !sameRef(v.art.Reference, art.Reference) {
						return nil, v, nil, changesErr("source_mismatch", "ambiguous workspace bundles")
					}
					copy := art
					v.art = &copy
				}
			}
			incoming := changesWorkspace(a, "before")
			unchanged := incoming.HeadCommit == w.HeadCommit && incoming.BaseCommit == w.BaseCommit && incoming.EmptyBase == w.EmptyBase
			if a.in.Restore != nil && (edge == "before" || (v.art == nil && unchanged)) {
				art := Artifact{Name: gitstate.ThinPackArtifactName, Reference: artifacts.NewStoredRef(*a.in.Restore), Size: a.in.Restore.SizeBytes, Task: a.task.ID, Attempt: a.task.Attempt}
				if v.art != nil && !sameRef(v.art.Reference, art.Reference) {
					return nil, v, nil, changesErr("source_mismatch", "incoming bundle references disagree")
				}
				v.art = &art
			}
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		return nil, changesCandidate{}, nil, changesErr("workspace_unavailable", "no authoritative main-workspace state")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].order < candidates[j].order })
	latest := candidates[len(candidates)-1]
	if len(candidates) > 1 && candidates[len(candidates)-2].order == latest.order {
		return nil, latest, nil, changesErr("workspace_ambiguous", "main-workspace execution order is ambiguous")
	}
	w := latest.w
	if latest.a.task.Attempt <= 0 || (!w.EmptyBase && w.HeadCommit == "") || (w.BaseCommit == "" && !w.EmptyBase && q.Base == "") {
		return nil, latest, nil, changesErr("workspace_unavailable", "latest main workspace has no recorded endpoints")
	}
	if (w.BaseCommit != "" && !fullHash(w.BaseCommit)) || (w.HeadCommit != "" && !fullHash(w.HeadCommit)) || (w.EmptyBase && w.BaseCommit != "") {
		return nil, latest, nil, changesErr("invalid_objects", "invalid recorded endpoint state")
	}
	if w.BaseCommit != "" && w.HeadCommit != "" && len(w.BaseCommit) != len(w.HeadCommit) {
		return nil, latest, nil, changesErr("invalid_objects", "endpoint object formats disagree")
	}
	if q.Base != "" && !fullHash(q.Base) {
		return nil, latest, nil, changesErr("invalid_request", "comparison base requires a full hash")
	}
	// An unchanged state may reuse an earlier exact candidate, never a different
	// head or base. Both the state producer and byte producer remain visible.
	if latest.art == nil {
		for i := len(candidates) - 2; i >= 0; i-- {
			latest.lineageScans++
			if latest.lineageScans > c.config.Limits.LineageCandidates {
				return nil, latest, nil, changesLimit("lineage_candidates")
			}
			v := candidates[i]
			if v.art != nil && v.w.HeadCommit == w.HeadCommit && v.w.BaseCommit == w.BaseCommit && v.w.EmptyBase == w.EmptyBase {
				latest.art = v.art
				break
			}
		}
	}
	if w.HeadCommit != "" && w.HeadCommit != w.BaseCommit && latest.art == nil {
		return nil, latest, nil, changesErr("missing_source", "latest recorded candidate has no applicable retained bundle")
	}
	return h, latest, candidates, nil
}

func candidateProducer(job string, v changesCandidate) ChangesProducer {
	return ChangesProducer{JobID: job, Task: v.w.Task, Attempt: v.w.Attempt}
}

func changesBaseOrigin(v changesCandidate) string {
	if v.w.EmptyBase {
		return "intentional_empty"
	}
	if v.w.Boundary == "after" && v.a.activity.Git.ParentHash != "" {
		return "output.parent_hash"
	}
	if v.a.in.Git.ParentHash != "" {
		return "invocation.parent_hash"
	}
	if v.w.Boundary == "after" && v.a.activity.WorkspaceAfter != nil {
		return "workspace_after.base_commit"
	}
	if v.w.Boundary == "before" && v.a.activity.WorkspaceBefore != nil {
		return "workspace_before.base_commit"
	}
	return "invocation.resolved_base_hash"
}

// Native external waits retain fixed inputs separately from attempt start.
// Changes uses those records without approximating a live local checkout.
func changesWaitingInputs(h *history) {
	done := map[string]bool{}
	for _, entry := range h.run.Execution {
		if entry.Record.Kind == "result" {
			done[entry.Record.ID] = true
		}
	}
	for order, entry := range h.run.Execution {
		r := entry.Record
		if r.Kind != "wait" || r.Input == nil || done[r.ID] {
			continue
		}
		split := strings.LastIndex(r.ID, "/attempt:")
		if split < 0 {
			continue
		}
		id := r.ID[:split]
		n, e := strconv.Atoi(r.ID[split+9:])
		if e != nil || n <= 0 {
			continue
		}
		as := h.attempts[id]
		var selected *attempt
		var keep []*attempt
		for _, a := range as {
			if a.task.Attempt == n {
				if selected == nil {
					selected = a
				}
			} else {
				keep = append(keep, a)
			}
		}
		if selected == nil {
			selected = &attempt{task: Task{ID: id, Attempt: n}}
		}
		if json.Unmarshal(r.Input.Data, &selected.in) != nil {
			continue
		}
		selected.task.Status = "waiting"
		selected.order = order
		selected.inputArtifacts = nil
		for _, art := range r.Input.Artifacts {
			if art.Key != nil {
				selected.inputArtifacts = append(selected.inputArtifacts, Artifact{Name: art.Name, Task: id, Attempt: n, Reference: artifacts.NewStoredRef(*art.Key), Size: art.Key.SizeBytes, Availability: "stored"})
			}
		}
		selected.finish()
		h.attempts[id] = append(keep, selected)
	}
}

func normalizeChangesRequest(q *ChangesRequest, l ChangesLimits) error {
	if strings.TrimSpace(q.JobID) == "" {
		return changesErr("invalid_request", "job ID is required")
	}
	if q.Base != "" && !fullHash(q.Base) {
		return changesErr("invalid_request", "comparison base requires a full hash")
	}
	if q.Limit == 0 {
		q.Limit = min(100, l.PageSize)
	}
	if q.Limit < 1 || q.Limit > l.PageSize {
		return changesErr("invalid_request", "page size is outside configured limits")
	}
	if int64(len(q.Cursor)) > l.CursorBytes || int64(len(q.Selection)) > l.CursorBytes {
		return changesLimit("cursor_bytes")
	}
	return nil
}
