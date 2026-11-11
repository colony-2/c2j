package story

import (
	"context"
	"errors"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/story/internal/service"
)

type FinalManifestState = model.FinalManifestState
type FinalDocument = model.FinalDocument
type FinalDocuments = model.FinalDocuments
type GetFinalDocumentRequest = model.GetFinalDocumentRequest

const (
	FinalManifestPresent     = model.FinalManifestPresent
	FinalManifestEmpty       = model.FinalManifestEmpty
	FinalManifestAbsent      = model.FinalManifestAbsent
	FinalManifestNull        = model.FinalManifestNull
	FinalManifestUnsupported = model.FinalManifestUnsupported
	FinalManifestMalformed   = model.FinalManifestMalformed
)

var ErrFinalDocumentsUnsupported = errors.New("story service does not support final document projection")
var ErrFinalDocumentUnavailable = service.ErrFinalDocumentUnavailable

// FinalDocumentService is additive; existing Service implementations need not
// implement it. New returns a service supporting this sibling interface.
type FinalDocumentService interface {
	GetFinalDocuments(context.Context, GetWorkflowOutcomeRequest) (*FinalDocuments, error)
	GetFinalDocument(context.Context, GetFinalDocumentRequest) (*ArtifactData, error)
}

func GetFinalDocuments(ctx context.Context, s Service, req GetWorkflowOutcomeRequest) (*FinalDocuments, error) {
	reader, ok := s.(FinalDocumentService)
	if !ok {
		return nil, ErrFinalDocumentsUnsupported
	}
	return reader.GetFinalDocuments(ctx, req)
}
func GetFinalDocument(ctx context.Context, s Service, req GetFinalDocumentRequest) (*ArtifactData, error) {
	reader, ok := s.(FinalDocumentService)
	if !ok {
		return nil, ErrFinalDocumentsUnsupported
	}
	return reader.GetFinalDocument(ctx, req)
}
