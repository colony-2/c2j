package input_test

import (
	"context"
	"errors"
	"io"

	"github.com/colony-2/c2j/pkg/input"
	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

// Supply the application's configured engine. Keep one service across requests
// because the bounded immutable snapshot cache belongs to that service.
func ExampleNewHistoryService() {
	var engine jobworkflow.Engine // initialized by the embedding application
	service, err := input.NewHistoryService(input.HistoryConfig{
		Reader: &c2workflow.SWFWorkflowControl{Engine: engine},
	})
	if err != nil {
		panic(err)
	}
	_ = service
}

// The caller resolves tenant/job authorization before calling the library.
func readInputHistory(ctx context.Context, service input.HistoryService, tenant, job string) error {
	page, err := service.ListJobInputStory(ctx, input.ListJobInputStoryRequest{TenantID: tenant, JobID: job, PageSize: 50})
	if err != nil {
		return err
	}
	for _, entry := range page.Entries {
		// Presence is independent of null/false/zero, and Raw retains exact numbers.
		if entry.AcceptedResponse.Present {
			_ = entry.AcceptedResponse.Raw
		}
		detail, err := service.GetJobInputStoryEntry(ctx, input.GetJobInputStoryEntryRequest{TenantID: tenant, JobID: job, EntryID: entry.EntryID, SnapshotToken: page.SnapshotToken})
		if err != nil {
			return err
		}
		_ = detail.Actionability // separately observed live; submit must still recheck
		for _, member := range detail.RequestManifest {
			artifact, err := service.OpenJobInputStoryArtifact(ctx, input.OpenJobInputStoryArtifactRequest{TenantID: tenant, JobID: job, EntryID: entry.EntryID, ArtifactID: member.ArtifactID, SnapshotToken: page.SnapshotToken})
			if errors.Is(err, input.ErrArtifactUnavailable) {
				continue
			}
			if err != nil {
				return err
			}
			_, readErr := io.Copy(io.Discard, artifact)
			closeErr := artifact.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	if page.NextPageToken != "" {
		_, err = service.ListJobInputStory(ctx, input.ListJobInputStoryRequest{TenantID: tenant, JobID: job, PageSize: 50, PageToken: page.NextPageToken})
		if errors.Is(err, input.ErrHistorySnapshotExpired) {
			// The application restarts pagination and replaces its loaded range.
			return err
		}
	}
	return err
}
