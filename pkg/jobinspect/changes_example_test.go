package jobinspect_test

import (
	"context"
	"errors"
	"io"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func ExampleNewChangesService() {
	// Supply your authenticated runtime and job authorization gate.
	var runtime jobdb.WorkflowRuntime
	var authorize func(context.Context, string) error
	service := &jobinspect.Service{Runtime: runtime, Tenant: "tenant", Authorize: authorize}
	_, _ = jobinspect.NewChangesService(service, jobinspect.ChangesConfig{}) // offline
	_, _ = jobinspect.NewChangesService(service, jobinspect.ChangesConfig{ResolveRepository: func(ctx context.Context, recorded string) (jobinspect.RepositoryAccess, error) {
		// Resolve only identities explicitly allowed by application policy.
		if recorded != "https://example.test/project.git" {
			return jobinspect.RepositoryAccess{}, &jobinspect.ChangesError{Version: 1, Code: "repository_denied", Detail: "repository is not configured"}
		}
		return jobinspect.RepositoryAccess{URL: "https://example.test/project.git"}, nil
	}})
}

func ExampleChangesService_History() {
	var api jobinspect.ChangesAPI
	ctx := context.Background()
	request := jobinspect.ChangesRequest{JobID: "job", Limit: 50}
	for {
		page, err := api.History(ctx, request)
		if err != nil {
			var diagnostic *jobinspect.ChangesError
			if errors.As(err, &diagnostic) && diagnostic.Code == "stale_selection" { /* discard cursor; refresh without selection */
			}
			return
		}
		if !page.Complete {
			return
		}
		if page.NextCursor == "" {
			break
		}
		request.Cursor = page.NextCursor
	}
}

func ExampleChangesService_OpenDiff() {
	var api jobinspect.ChangesAPI
	ctx := context.Background()
	page, err := api.History(ctx, jobinspect.ChangesRequest{JobID: "job"})
	if err != nil || !page.Complete || len(page.Commits) == 0 {
		return
	}
	request := jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: "job", Selection: page.Selection}, From: page.Commits[0].Hash, Through: page.Commits[len(page.Commits)-1].Hash}
	_, body, err := api.OpenDiff(ctx, request)
	if err != nil {
		return
	}
	defer body.Close()
	_, copyErr := io.Copy(io.Discard, body)
	closeErr := body.Close()
	if copyErr != nil || closeErr != nil { /* discard incomplete output */
		return
	}
}
