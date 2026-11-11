package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

var ErrFinalDocumentUnavailable = errors.New("final document has no resolved stored producer")

// GetFinalDocuments projects only the selected terminal attempt's declaration.
// It never reads bytes, fetches external URLs or replays producer jobs.
func (s *Service) GetFinalDocuments(ctx context.Context, req model.GetWorkflowOutcomeRequest) (*model.FinalDocuments, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}
	project, job := strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.JobID)
	if project == "" {
		return nil, ErrInvalidProject
	}
	if job == "" {
		return nil, ErrNotFound
	}
	run, err := s.engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: jobdb.JobKey{TenantId: project, JobId: job}, IncludeOutputs: true, IncludeArtifacts: true})
	if err != nil {
		if errors.Is(err, jobdb.ErrJobNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if mapWorkflowStatus(run.Job.Status) == model.WorkflowStatusRunning {
		return nil, ErrOutcomePending
	}
	var latest *jobdb.JobAttempt
	if len(run.Attempts) > 0 {
		latest = &run.Attempts[len(run.Attempts)-1]
	}
	return finalDocuments(job, latest), nil
}

func finalDocuments(job string, attempt *jobdb.JobAttempt) *model.FinalDocuments {
	out := &model.FinalDocuments{JobID: job, State: model.FinalManifestAbsent, Documents: []model.FinalDocument{}}
	if attempt == nil {
		return out
	}
	ordinal := attempt.Ordinal
	out.AttemptOrdinal = &ordinal
	if attempt.Output == nil || len(attempt.Output.Data) == 0 {
		return out
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(attempt.Output.Data, &fields) != nil || fields == nil {
		out.State = model.FinalManifestMalformed
		out.Diagnostic = "terminal output is not an object"
		return out
	}
	raw, ok := fields["artifact_refs"]
	if !ok {
		return out
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		out.State = model.FinalManifestNull
		return out
	}
	var manifest map[string]json.RawMessage
	if json.Unmarshal(raw, &manifest) != nil || manifest == nil {
		out.State = model.FinalManifestUnsupported
		out.Diagnostic = "artifact_refs must be an object"
		return out
	}
	if len(manifest) == 0 {
		out.State = model.FinalManifestEmpty
		return out
	}
	out.State = model.FinalManifestPresent
	// Only the containing terminal result's attachment map can bind an attached
	// ref. Forwarded keys already retain the original producer coordinates.
	attachments := map[string]artifacts.Ref{}
	for _, a := range attempt.Output.Artifacts {
		if a.Key != nil {
			attachments[a.Name] = artifacts.NewStoredRef(*a.Key)
		}
	}
	names := make([]string, 0, len(manifest))
	for name := range manifest {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := manifest[name]
		d := model.FinalDocument{Alias: name, Raw: append(json.RawMessage(nil), raw...), Resolution: "unresolved"}
		if err := json.Unmarshal(raw, &d.Ref); err != nil {
			d.Diagnostic = err.Error()
			out.State = model.FinalManifestMalformed
		} else if err := d.Ref.Validate(); err != nil {
			d.Diagnostic = err.Error()
			switch d.Ref.Kind {
			case "", artifacts.RefKindStored, artifacts.RefKindAttached, artifacts.RefKindExternal, artifacts.RefKindValue:
				out.State = model.FinalManifestMalformed
			default:
				if out.State != model.FinalManifestMalformed {
					out.State = model.FinalManifestUnsupported
				}
			}
		} else {
			resolved, reason := resolveFinalRef(d.Ref, attachments)
			d.Diagnostic = reason
			if key, ok := resolved.StoredKey(); ok && key.Validate() == nil {
				d.Producer = &key
				d.Resolution = "stored"
			} else if d.Ref.Kind == artifacts.RefKindExternal {
				d.Resolution = "external"
			} else if d.Ref.Kind == artifacts.RefKindValue {
				d.Resolution = "value"
			}
		}
		out.Documents = append(out.Documents, d)
	}
	return out
}

// Bound attachment chains and preserve cycle diagnostics. No filename search in
// other results is allowed.
func resolveFinalRef(ref artifacts.Ref, containing map[string]artifacts.Ref) (artifacts.Ref, string) {
	seen := map[string]bool{}
	for depth := 0; depth < 64; depth++ {
		if err := ref.Validate(); err != nil {
			return artifacts.Ref{}, err.Error()
		}
		if ref.Kind != artifacts.RefKindAttached {
			return ref, ""
		}
		name := ref.NameValue()
		if seen[name] {
			return artifacts.Ref{}, "cyclic containing-result reference"
		}
		seen[name] = true
		next, ok := containing[name]
		if !ok {
			return artifacts.Ref{}, "containing result attachment map has no recorded producer"
		}
		ref = next
	}
	return artifacts.Ref{}, "containing-result reference depth exceeded"
}

// GetFinalDocument reads through the existing producer transport. Membership is
// reselected from the terminal manifest; backend denial/storage errors propagate.
func (s *Service) GetFinalDocument(ctx context.Context, req model.GetFinalDocumentRequest) (*model.ArtifactData, error) {
	manifest, err := s.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: req.ProjectID, JobID: req.JobID})
	if err != nil {
		return nil, err
	}
	for _, d := range manifest.Documents {
		if d.Alias != req.Alias {
			continue
		}
		if d.Producer == nil {
			return nil, fmt.Errorf("%w: %s", ErrFinalDocumentUnavailable, d.Diagnostic)
		}
		k := d.Producer
		return s.GetArtifactByOrdinal(ctx, model.GetArtifactByOrdinalRequest{ProjectID: req.ProjectID, JobID: k.JobId, TaskOrdinal: k.TaskOrdinal, ArtifactName: k.Name})
	}
	return nil, ErrNotFound
}
