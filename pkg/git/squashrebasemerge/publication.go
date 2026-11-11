package squashrebasemerge

// PublicationDisposition records evidence at execution, not current remote state.
// The empty historical value never proves publication.
type PublicationDisposition string

const (
	PublicationPublished      PublicationDisposition = "published"
	PublicationAlreadyPresent PublicationDisposition = "already_present"
	PublicationNoChanges      PublicationDisposition = "no_changes"
)

// PublicationEvidence is additive to native and combined merge outputs.
type PublicationEvidence struct {
	CandidateSHA           string                 `json:"candidate_sha,omitempty"`
	CandidateSubject       string                 `json:"candidate_subject"`
	PublicationBranch      string                 `json:"publication_branch,omitempty"`
	PublicationDisposition PublicationDisposition `json:"publication_disposition,omitempty"`
}
