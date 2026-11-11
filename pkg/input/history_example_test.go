package input_test

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/story"
	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
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

// Called only after the user explicitly selects a history entry and requests a
// restart. Authorization belongs to the embedding application, separately from
// reading history. The response identifies the new job; keep the old history.
func restartSavedInput(ctx context.Context, restarts story.Service, page *input.JobInputStoryPage, entry input.JobInputStoryEntry, requestID string) (*story.RestartRecipeJobResponse, error) {
	target := entry.Locator.Restart
	if target.UnavailableReason != "" {
		return nil, fmt.Errorf("restart unavailable: %s", target.UnavailableReason)
	}
	if target.ScopeID == "" {
		return nil, errors.New("no recorded restart scope")
	}
	// For containing/body relationships, show the enclosing work to be rerun.
	return restarts.RestartRecipeJob(ctx, story.RestartRecipeJobRequest{
		ProjectID: page.TenantID, JobID: page.JobID, ScopeID: target.ScopeID, RequestID: requestID,
	})
}

func ExampleRecordedHistoryRestart() {
	// The application already holds authorized retained records for this job.
	key := jobdb.JobKey{TenantId: "tenant", JobId: "selected-job"}
	scope := `/scope:"review"#1`
	invocation := scope + `/attempt:1/task:"input:generate_form"#1/attempt:1`
	ordinal := int64(7)
	run := jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{JobKey: key}, Execution: []jobdb.ExecutionHistoryEntry{
		{Ordinal: 7, Record: jobdb.ExecutionRecord{Version: jobdb.ExecutionPatternVersion, Kind: "scope", ID: scope, Descriptor: []byte(`{"restart":true}`)}},
		{Ordinal: 7, Record: jobdb.ExecutionRecord{Version: jobdb.ExecutionPatternVersion, Kind: "result", ID: invocation}},
	}}
	target := input.RecordedHistoryRestart(key, run, input.HistoryLocator{InvocationID: invocation, GenerationOrdinal: &ordinal})
	if target.UnavailableReason != "" {
		fmt.Println(target.UnavailableReason)
		return
	}
	fmt.Println(target.ScopeID, target.Relationship)
	// Output: /scope:"review"#1 containing
}

// Capture merges and event positions from the page's exact retained observation,
// including when Entries is empty. Continue input pagination with NextPageToken.
func ExampleGetJobHistorySnapshot() {
	var service input.HistoryService // initialized by NewHistoryService
	ctx := context.Background()
	page, err := service.ListJobInputStory(ctx, input.ListJobInputStoryRequest{TenantID: "tenant", JobID: "job"})
	if err != nil {
		return
	}
	snapshot, err := input.GetJobHistorySnapshot(ctx, service, input.GetJobHistorySnapshotRequest{TenantID: "tenant", JobID: "job", SnapshotToken: page.SnapshotToken})
	if err != nil {
		return
	}
	for _, merge := range snapshot.Merges {
		verified := merge.EvidenceAvailability == "recorded" && (merge.PublicationDisposition == "published" || merge.PublicationDisposition == "already_present")
		_ = verified               // no_changes is recorded but never verified; no claim of current remote membership
		_ = merge.CandidateSubject // optional even for verified records: absent/null is nil, empty stays non-nil; never reconstruct
	}
	_ = snapshot.Events // ordinal, invocation_id, kind, entry_id order
}
