package live

import (
	"encoding/json"

	"github.com/colony-2/c2j/pkg/story/internal/inbox"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Inbox attachments enrich the story but are not required to replay its events.
// Rebind their durable keys to the inspection API so a denied or missing optional
// attachment cannot trip the execution reader's infrastructure latch.
func (r *Recorder) projectTaskInbox(input jobdb.TaskData) *model.Inbox {
	if r.inboxGetter == nil || input == nil {
		return inbox.Project(r.ctx, input)
	}
	raw, err := input.GetData()
	if err != nil {
		return inbox.Unavailable(inbox.Reason(inbox.ReadFailure("history", err)))
	}
	data := &jobdb.SimpleTaskData{Data: raw}
	var envelope struct {
		InputArtifact string `json:"input_artifact"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.InputArtifact == "" {
		return inbox.Project(r.ctx, data)
	}
	attachments, err := input.GetArtifacts()
	if err != nil {
		return inbox.Unavailable(inbox.Reason(inbox.ReadFailure("attachment", err)))
	}
	for _, attachment := range attachments {
		if attachment == nil || attachment.Name() != envelope.InputArtifact {
			continue
		}
		key, err := attachment.ArtifactKey()
		if err != nil {
			return inbox.Unavailable("attachment_missing")
		}
		data.Artifacts = append(data.Artifacts, (jobdb.ExecutionArtifact{Name: attachment.Name(), Key: &key}).ToArtifact(r.inboxGetter, r.jobKey.TenantId))
	}
	return inbox.Project(r.ctx, data)
}
