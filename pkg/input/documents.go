package input

import (
	"context"
	"io"

	"github.com/colony-2/c2j/pkg/artifacts"
)

// ReviewDocument is an opened, immutable stored document. Close it after use.
// Ref retains its original producer identity. Completion of the review does not
// revoke an already opened stream.
type ReviewDocument struct {
	ID        string
	Ref       artifacts.Ref
	Name      string
	SizeBytes int64
	io.ReadCloser
}

// OpenReviewDocument resolves only document IDs belonging to the exact pending
// review occurrence in the current tenant. It accepts no external paths or URLs.
func (r *Runtime) OpenReviewDocument(ctx context.Context, tenantID, jobID, requestID, documentID string) (*ReviewDocument, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	form, err := r.GetForm(ctx, tenantID, jobID)
	if err != nil {
		return nil, err
	}
	if form.RequestID == "" || requestID != form.RequestID {
		return nil, runtimeError(ErrStaleRequest, "open review document", nil)
	}
	if form.Kind != "review" {
		return nil, validationError("form.kind", "input is not a review")
	}
	ref, ok := form.Documents[documentID]
	if !ok {
		return nil, &RuntimeError{Kind: ErrDocumentNotFound, DocumentID: documentID}
	}
	key, ok := ref.StoredKey()
	if !ok {
		return nil, invalidFormError(validationError("documents", "expected a stored artifact"))
	}
	artifact := r.ctl.GetArtifactLazy(ctx, tenantID, key)
	if artifact == nil {
		return nil, &RuntimeError{Kind: ErrArtifactUnavailable, DocumentID: documentID}
	}
	reader, err := artifact.Open()
	if err != nil {
		return nil, documentReadError(documentID, err)
	}
	return &ReviewDocument{ID: documentID, Ref: ref, Name: ref.NameValue(), SizeBytes: key.SizeBytes,
		ReadCloser: &reviewDocumentReader{ReadCloser: reader, ctx: ctx, id: documentID}}, nil
}

type reviewDocumentReader struct {
	io.ReadCloser
	ctx context.Context
	id  string
}

func (r *reviewDocumentReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = documentReadError(r.id, err)
	}
	return n, err
}
func documentReadError(id string, err error) error {
	classified := runtimeError(ErrArtifactUnavailable, "read review document", err)
	if typed, ok := classified.(*RuntimeError); ok {
		typed.DocumentID = id
	}
	return classified
}
