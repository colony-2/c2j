package service

import (
	"context"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/story/internal/model"
	storylive "github.com/colony-2/c2j/pkg/story/live"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type storyErrorEngine struct {
	jobworkflow.Engine
	cause error
}

func (e storyErrorEngine) GetJobRun(context.Context, jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	return jobdb.GetJobRunResponse{}, nil
}
func (e storyErrorEngine) ReplayJobRun(context.Context, jobworkflow.ReplayRunRequest) (jobdb.JobData, error) {
	return nil, e.cause
}
func TestStoryServicePreservesReconstructionErrorCauses(t *testing.T) {
	sourceErr := errors.New("stored recipe cannot be loaded")
	for _, cause := range []error{sourceErr, errors.Join(jobdb.ErrWorkflowNotDeterministic, storylive.ErrIncompleteReplay, sourceErr)} {
		svc, err := New(Config{Engine: storyErrorEngine{cause: cause}})
		require.NoError(t, err)
		_, err = svc.GetJobRunStory(context.Background(), model.GetJobRunStoryRequest{ProjectID: "tenant", JobID: "job"})
		require.ErrorIs(t, err, sourceErr)
		if errors.Is(cause, jobdb.ErrWorkflowNotDeterministic) {
			require.ErrorIs(t, err, ErrJobRunStoryMismatch)
			require.ErrorIs(t, err, storylive.ErrIncompleteReplay)
		}
	}
}
