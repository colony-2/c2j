package model

import (
	"encoding/json"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type FinalManifestState string

const (
	FinalManifestPresent     FinalManifestState = "present"
	FinalManifestEmpty       FinalManifestState = "empty"
	FinalManifestAbsent      FinalManifestState = "absent"
	FinalManifestNull        FinalManifestState = "null"
	FinalManifestUnsupported FinalManifestState = "unsupported"
	FinalManifestMalformed   FinalManifestState = "malformed"
)

// FinalDocument describes declared membership independently of byte availability.
// Producer is nil for unresolved, external and value references.
type FinalDocument struct {
	Alias      string             `json:"alias"`
	Ref        artifacts.Ref      `json:"ref"`
	Raw        json.RawMessage    `json:"raw"`
	Resolution string             `json:"resolution"`
	Producer   *jobdb.ArtifactKey `json:"producer,omitempty"`
	Diagnostic string             `json:"diagnostic,omitempty"`
}
type FinalDocuments struct {
	JobID          string             `json:"job_id"`
	AttemptOrdinal *int64             `json:"attempt_ordinal,omitempty"`
	State          FinalManifestState `json:"state"`
	Documents      []FinalDocument    `json:"documents"`
	Diagnostic     string             `json:"diagnostic,omitempty"`
}
type GetFinalDocumentRequest struct {
	ProjectID, JobID, Alias string
}
