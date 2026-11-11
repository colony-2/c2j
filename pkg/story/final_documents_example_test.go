package story_test

import (
	"context"
	"errors"
	"github.com/colony-2/c2j/pkg/story"
)

// Applications authorize the selected job and producer access before transporting
// bytes. A declared member stays in the manifest even if reading it fails.
func ExampleGetFinalDocuments() {
	var service story.Service // initialized by story.New
	ctx := context.Background()
	manifest, err := story.GetFinalDocuments(ctx, service, story.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: "job"})
	if errors.Is(err, story.ErrOutcomePending) {
		return
	}
	if err != nil {
		return
	}
	if manifest.State == story.FinalManifestEmpty {
		return
	}
	for _, document := range manifest.Documents {
		if document.Producer == nil {
			continue
		} // unresolved, external, value
		data, err := story.GetFinalDocument(ctx, service, story.GetFinalDocumentRequest{ProjectID: "tenant", JobID: "job", Alias: document.Alias})
		if err != nil {
			return
		} // preserve the backend error, including missing bytes
		_ = data.Content
		_ = document.Producer.Name // storage name, distinct from display Alias
	}
}
