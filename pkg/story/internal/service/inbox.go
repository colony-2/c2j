package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/inbox"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// GetInboxValue returns bytes only for a recorded value selection. Stored refs
// use the existing artifact API. It never invokes replay or expression evaluation.
func (s *Service) GetInboxValue(ctx context.Context, req model.GetInboxValueRequest) (*model.InboxValue, error) {
	if s.engine == nil {
		return nil, ErrEngineUnavailable
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, ErrInvalidProject
	}
	if strings.TrimSpace(req.JobID) == "" || req.JobAttempt <= 0 || req.TaskOrdinal <= 0 || req.TaskAttempt <= 0 || req.Binding == "" {
		return nil, inbox.Failure("invalid_selector")
	}
	key := jobdb.JobKey{TenantId: req.ProjectID, JobId: req.JobID}
	run, err := s.engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeAttemptInputs: true, IncludeInputs: true, IncludeArtifacts: true})
	if err != nil {
		return nil, inbox.ReadFailure("history", err)
	}
	if run.Job.JobKey != key {
		return nil, ErrWorkflowNotInProject
	}

	td, err := s.inboxTaskData(req, run)
	if err != nil {
		return nil, err
	}
	refs, err := inbox.Decode(ctx, td)
	if err != nil {
		return nil, err
	}
	ref, ok := refs[req.Binding]
	if !ok {
		return nil, inbox.Failure("binding_unknown")
	}
	switch ref.Kind {
	case artifacts.RefKindStored:
		if ref.Stored == nil || ref.Stored.Key.Validate() != nil {
			return nil, inbox.Failure("descriptor_malformed")
		}
		return nil, inbox.Failure("stored_reference")
	case artifacts.RefKindValue:
		if ref.Format != "text" && ref.Format != "json" {
			return nil, inbox.Failure("format_unsupported")
		}
		data, err := ref.ValueBytes()
		if err != nil {
			return nil, inbox.Failure("descriptor_malformed")
		}
		filename := inbox.Destination(req.Binding, ref.NameValue())
		if filename == "" {
			filename = ref.NameValue()
		}

		return &model.InboxValue{Data: data, Filename: filename, Format: ref.Format}, nil
	default:
		return nil, inbox.Failure("source_unsupported")
	}
}

func (s *Service) inboxTaskData(req model.GetInboxValueRequest, run jobdb.GetJobRunResponse) (jobdb.TaskData, error) {
	if len(run.Execution) > 0 {
		if req.InvocationID == "" {
			return nil, inbox.Failure("invalid_selector")
		}
		suffix := fmt.Sprintf("/attempt:%d", req.TaskAttempt)
		id := strings.TrimSuffix(req.InvocationID, suffix) + suffix
		prefix := fmt.Sprintf(`/scope:"$job"#1/attempt:%d/`, req.JobAttempt)
		if !strings.HasPrefix(id, prefix) || !strings.Contains(id, "/task:") {
			return nil, inbox.Failure("invalid_selector")
		}
		var selected *jobdb.ExecutionSnapshot
		matches := 0
		for _, entry := range run.Execution {
			if entry.Record.Kind == "result" && entry.Record.ID == id && entry.Ordinal == req.TaskOrdinal {
				selected = entry.Record.Input
				matches++
			}
		}
		if matches > 1 {
			return nil, inbox.Failure("invalid_selector")
		}
		if matches == 0 || selected == nil {
			return nil, inbox.Failure("history_missing")
		}
		td := &jobdb.SimpleTaskData{Data: selected.Data}
		var envelope struct {
			InputArtifact string `json:"input_artifact"`
		}
		if json.Unmarshal(selected.Data, &envelope) != nil {
			return nil, inbox.Failure("history_malformed")
		}
		for _, art := range selected.Artifacts {
			if art.Name == envelope.InputArtifact && envelope.InputArtifact != "" {
				if art.Key == nil {
					return nil, inbox.Failure("attachment_missing")
				}
				td.Artifacts = append(td.Artifacts, art.ToArtifact(s.engine, req.ProjectID))
			}
		}
		return td, nil
	}
	var selected *jobdb.TaskIO
	matches := 0
	for _, ja := range run.Attempts {
		if ja.Attempt != req.JobAttempt {
			continue
		}
		for _, task := range ja.Tasks {
			for _, attempt := range task.Attempts {
				if attempt.Ordinal == req.TaskOrdinal && attempt.Attempt == req.TaskAttempt {
					selected = attempt.Input
					matches++
				}
			}
		}
	}
	if matches > 1 {
		return nil, inbox.Failure("invalid_selector")
	}
	if matches == 0 || selected == nil {
		return nil, inbox.Failure("history_missing")
	}
	td := &jobdb.SimpleTaskData{Data: selected.Data}
	var envelope struct {
		InputArtifact string `json:"input_artifact"`
	}
	if json.Unmarshal(selected.Data, &envelope) != nil {
		return nil, inbox.Failure("history_malformed")
	}
	for _, info := range selected.Artifacts {
		if info.Name != envelope.InputArtifact || envelope.InputArtifact == "" {
			continue
		}
		if info.Key == nil {
			return nil, inbox.Failure("attachment_missing")
		}
		art, err := s.engine.GetArtifact(req.ProjectID, *info.Key)
		if err != nil {
			return nil, inbox.ReadFailure("attachment", err)
		}
		td.Artifacts = append(td.Artifacts, art)
	}
	return td, nil
}
