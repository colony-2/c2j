package jobinspect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strings"

	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

var _ WorkspaceDiffAPI = (*Service)(nil)

// OpenWorkspaceDiff reads a saved comparison, without invoking Git or loading
// a recipe. Metadata and source selection share one immutable observation.
func (s *Service) OpenWorkspaceDiff(ctx context.Context, q WorkspaceDiffRequest) (*WorkspaceDiff, io.ReadCloser, error) {
	m, err := s.workspaceDiff(ctx, q)
	if err != nil || m.Status == "unavailable" {
		return m, nil, err
	}
	if m.Source == nil {
		return m, io.NopCloser(bytes.NewReader(nil)), nil
	}
	r, err := s.openWorkspacePatch(ctx, m.Source.Key)
	if workspacePatchMissing(err) {
		diffUnavailable(m, "saved patch bytes are no longer retained")
		return m, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return m, NewWorkspaceDiffStream(ctx, r, m.Bytes), nil
}

func workspacePatchMissing(err error) bool {
	if err == nil {
		return false
	}
	status, _, typed := jobdb.CellErrorStatus(err)
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, jobdb.ErrChapterNotFound) || (typed && status == 404)
}

func (s *Service) workspaceDiff(ctx context.Context, q WorkspaceDiffRequest) (*WorkspaceDiff, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	// Copy pointer selectors: callers cannot mutate the observation's request.
	raw, _ := json.Marshal(q)
	var requested WorkspaceDiffRequest
	_ = json.Unmarshal(raw, &requested)
	o, err := s.observeWorkspace(ctx, q.JobID)
	if err != nil {
		return nil, err
	}
	d := newDiffHistory(o.h)
	m := &WorkspaceDiff{Version: WorkspaceDiffVersion, Status: "unavailable", Requested: requested, Observation: o.id, Cutoff: workspaceCutoff(o.id, len(o.h.run.Execution)-1)}
	v, err := d.selectState(q, m)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return m, nil
	}
	m.Workspace = &v.w
	m.StateProducer = diffProducer(q.JobID, v.a)
	if v.issue != "" {
		diffUnavailable(m, v.issue)
		return m, nil
	}
	if !diffStateValid(v.w) {
		diffUnavailable(m, "recorded boundary has missing or contradictory workspace endpoints")
		return m, nil
	}
	if !v.before {
		art, reason := d.savedPatch(v)
		if strings.HasPrefix(reason, "conflicting") || strings.HasPrefix(reason, "invalid") {
			diffUnavailable(m, reason)
			return m, nil
		}
		if art != nil {
			return s.bindWorkspacePatch(ctx, d, v, m, art)
		}
		// A missing patch for a changed state must not trigger older-state
		// substitution. Reuse is possible only when this exact input carried it.
		before := incomingDiffState(v.a)
		if before == nil || !sameDiffState(*before, v.w) {
			if diffEmpty(v) {
				m.Status = "empty"
				zero := int64(0)
				m.Bytes = &zero
				return m, nil
			}
			diffUnavailable(m, reason)
			return m, nil
		}
	}
	for prior := d.predecessor(v); prior != nil; prior = d.predecessor(prior) {
		if !sameDiffState(prior.w, v.w) {
			break
		}
		if prior.issue != "" {
			diffUnavailable(m, prior.issue)
			return m, nil
		}
		art, reason := d.savedPatch(prior)
		if strings.HasPrefix(reason, "conflicting") || strings.HasPrefix(reason, "invalid") {
			diffUnavailable(m, reason)
			return m, nil
		}
		if art != nil {
			return s.bindWorkspacePatch(ctx, d, prior, m, art)
		}
	}
	if incoming := incomingBundle(v.a); incoming != nil && incoming.JobId != q.JobID {
		o, err := s.observeWorkspace(ctx, incoming.JobId)
		if err != nil {
			return nil, err
		}
		foreign := newDiffHistory(o.h)
		for _, a := range foreign.tasks {
			if k := diffBundle(a); k != nil && *k == *incoming && k.TaskOrdinal == a.ordinal && k.JobId == o.h.job.ID {
				prior := foreign.state(a, false)
				if prior == nil || !sameDiffEndpoints(prior.w, &v.w) {
					diffUnavailable(m, "foreign snapshot endpoints do not match selected state")
					return m, nil
				}
				if prior.issue != "" {
					diffUnavailable(m, prior.issue)
					return m, nil
				}
				art, reason := foreign.savedPatch(prior)
				if strings.HasPrefix(reason, "conflicting") || strings.HasPrefix(reason, "invalid") {
					diffUnavailable(m, reason)
					return m, nil
				}
				if art != nil {
					return s.bindWorkspacePatch(ctx, foreign, prior, m, art)
				}
			}
		}
	}
	if diffEmpty(v) {
		m.Status = "empty"
		zero := int64(0)
		m.Bytes = &zero
		return m, nil
	}
	diffUnavailable(m, "selected state has no eligible saved cumulative patch in its incoming lineage")
	return m, nil
}
func diffEmpty(v *diffState) bool {
	return (v.w.BaseCommit != "" && v.w.HeadCommit == v.w.BaseCommit) || (v.w.EmptyBase && v.w.HeadCommit == "")
}

func (s *Service) bindWorkspacePatch(ctx context.Context, d *diffHistory, v *diffState, m *WorkspaceDiff, art *Artifact) (*WorkspaceDiff, error) {
	k := art.Reference.Stored.Key
	if k.Validate() != nil || k.Name != art.Name {
		diffUnavailable(m, "invalid saved patch reference")
		return m, nil
	}
	producer := v.a
	if k.JobId != d.h.job.ID || k.TaskOrdinal != producer.ordinal {
		diffUnavailable(m, "saved reference is not owned by the recorded original producer")
		return m, nil
	}
	// Validate exact physical chapter membership; self-asserted output refs
	// and matching filenames cannot manufacture original producer ownership.
	found := false
	for _, a := range producer.task.Artifacts {
		if a.Reference.Stored != nil && a.Reference.Stored.Key == k && a.Name == k.Name {
			found = true
		}
	}
	if !found {
		diffUnavailable(m, "patch does not belong to the original producer")
		return m, nil
	}
	key := jobdb.JobKey{TenantId: s.Tenant, JobId: k.JobId}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, k.JobId); err != nil {
			return nil, WorkspaceDiffDiagnostic("denied", "original producer inspection denied", err)
		}
	}
	chapter, err := s.Runtime.GetChapter(ctx, jobdb.ChapterRef{JobKey: key, Ordinal: k.TaskOrdinal})
	if workspacePatchMissing(err) {
		diffUnavailable(m, "original patch chapter is no longer retained")
		return m, nil
	}
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("storage", "cannot validate original patch membership", err)
	}
	member := false
	for _, stored := range chapter.Artifacts {
		if stored.Name == k.Name {
			if k.SizeBytes >= 0 && stored.Size >= 0 && k.SizeBytes != stored.Size {
				return nil, WorkspaceDiffDiagnostic("protocol", "saved patch size metadata disagrees", nil)
			}
			member = true
		}
	}
	if !member {
		diffUnavailable(m, "saved patch is absent from the exact original chapter inventory")
		return m, nil
	}
	// ArtifactReader is the existing exact-reference storage port. The current
	// HTTP backend eagerly buffers it; no new backend capability is required.
	r, err := s.openWorkspacePatch(ctx, k)
	if workspacePatchMissing(err) {
		diffUnavailable(m, "saved patch bytes are no longer retained")
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err = r.Close(); err != nil {
		return nil, WorkspaceDiffDiagnostic("close", "cannot close saved patch probe", err)
	}
	m.Source = &WorkspaceDiffSource{Producer: *diffProducer(k.JobId, producer), Key: k}
	m.Reasons = nil
	m.Status = "available"
	if known := r.(*workspacePatchReader).size; known >= 0 {
		size := known
		m.Bytes = &size
		if size == 0 {
			m.Status = "empty"
		}
	}
	return m, nil
}
func sameDiffEndpoints(a coretask.WorkspaceState, b *coretask.WorkspaceState) bool {
	return b != nil && sameChangesRepository(a.Repository, b.Repository) && a.BaseCommit == b.BaseCommit && a.HeadCommit == b.HeadCommit && a.EmptyBase == b.EmptyBase
}

func (s *Service) openWorkspacePatch(ctx context.Context, k jobdb.ArtifactKey) (io.ReadCloser, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, WorkspaceDiffDiagnostic("canceled", "saved patch open canceled", err)
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, k.JobId); err != nil {
			return nil, WorkspaceDiffDiagnostic("denied", "saved patch access denied", err)
		}
	}
	chapter, err := s.Runtime.GetChapter(ctx, jobdb.ChapterRef{JobKey: jobdb.JobKey{TenantId: s.Tenant, JobId: k.JobId}, Ordinal: k.TaskOrdinal})
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("storage", "cannot recheck exact saved patch membership", err)
	}
	member := false
	expected := k.SizeBytes
	for _, art := range chapter.Artifacts {
		if art.Name == k.Name {
			if expected >= 0 && art.Size >= 0 && expected != art.Size {
				return nil, WorkspaceDiffDiagnostic("protocol", "saved patch size metadata disagrees", nil)
			}
			member = true
			if art.Size >= 0 {
				expected = art.Size
			}
		}
	}
	if !member {
		return nil, WorkspaceDiffDiagnostic("storage", "saved patch not retained in exact chapter", fs.ErrNotExist)
	}
	a, err := s.Runtime.OpenArtifact(ctx, jobdb.ArtifactRef{JobKey: jobdb.JobKey{TenantId: s.Tenant, JobId: k.JobId}, Ordinal: k.TaskOrdinal, Name: k.Name})
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("storage", "cannot open saved patch", err)
	}
	if a == nil || a.Name() != k.Name || a.Size() < -1 || (a.Size() >= 0 && expected >= 0 && a.Size() != expected) {
		return nil, WorkspaceDiffDiagnostic("protocol", "saved patch descriptor disagrees with exact reference", nil)
	}
	if a.Size() >= 0 {
		expected = a.Size()
	}
	r, err := a.Open()
	if err != nil {
		return nil, WorkspaceDiffDiagnostic("open", "cannot open saved patch stream", err)
	}
	if r == nil {
		return nil, WorkspaceDiffDiagnostic("protocol", "saved patch stream is missing", nil)
	}
	var size *int64
	if expected >= 0 {
		size = &expected
	}
	return &workspacePatchReader{ReadCloser: NewWorkspaceDiffStream(ctx, r, size), size: expected}, nil
}

type workspacePatchReader struct {
	io.ReadCloser
	size int64
}
