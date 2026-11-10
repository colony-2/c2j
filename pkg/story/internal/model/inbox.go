package model

import "github.com/colony-2/jobdb/pkg/jobdb"

// Inbox describes recorded selections, never verified delivery. Available metadata
// does not promise retained content. An absent Inbox means unavailable history.
type Inbox struct {
	Status  string       `json:"status"`
	Reason  string       `json:"reason,omitempty"`
	Entries []InboxEntry `json:"entries"`
}
type InboxEntry struct {
	Binding    string             `json:"binding"`
	Path       string             `json:"path,omitempty"`
	SourceKind string             `json:"source_kind"`
	SourceKey  *jobdb.ArtifactKey `json:"source_key,omitempty"`
	Content    string             `json:"content"` // value, stored, or unsupported
	Reason     string             `json:"reason,omitempty"`
}

// GetInboxValueRequest selects one durable task attempt. InvocationID is the
// story node identity (required for execution histories); TaskAttempt supplies
// its explicit retry. TaskOrdinal alone is accepted only for legacy task runs.
// JobAttempt must always be explicit. Binding is a recorded selection name.
type GetInboxValueRequest struct {
	ProjectID    string
	JobID        string
	JobAttempt   int
	InvocationID string
	TaskOrdinal  int64
	TaskAttempt  int
	Binding      string
}
type InboxValue struct {
	Data     []byte `json:"data"`
	Filename string `json:"filename"`
	Format   string `json:"format"`
}
