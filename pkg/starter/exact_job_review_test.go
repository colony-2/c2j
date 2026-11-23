package starter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExactJobDescriptorReservationPrecision(t *testing.T) {
	ctx := context.Background()
	start := workflowctl.StartJob{TenantId: "t", Cell: "app", CellRepo: "example.test/app", RecipeName: "build", Inputs: map[string]any{
		"positive": json.Number("9007199254740993"),
		"nested":   []any{map[string]any{"negative": json.Number("-9007199254740993"), "decimal": json.Number("12345678901234567890.123456789")}},
	}}
	job, err := PrepareRecipeJob(ctx, start, StartRecipeJobOptions{})
	require.NoError(t, err)
	require.Empty(t, job.JobID, "exercise destination reservation")
	descriptor, err := NewDescriptor(ctx, job)
	require.NoError(t, err)
	require.NotEmpty(t, descriptor.Job.JobID)
	wire, err := json.Marshal(descriptor)
	require.NoError(t, err)
	var restored Descriptor
	require.NoError(t, json.Unmarshal(wire, &restored))
	request, err := restored.Request()
	require.NoError(t, err)
	capture := &captureSubmitter{}
	_, err = restored.Submit(ctx, capture)
	require.NoError(t, err)
	for _, data := range []jobdb.TaskData{request.Data, capture.job.Data} {
		var got workflowctl.StartJob
		require.NoError(t, jsonexact.Unmarshal(data.GetDataOrPanic(), &got))
		require.Equal(t, start.Inputs, got.Inputs)
		require.Equal(t, descriptor.Job.JobID, got.JobID)
	}
}

func TestExactJobRawCELSubmissionNoWrites(t *testing.T) {
	rec, err := recipe.LoadRecipeFromString([]byte("id: build\nsequence:\n- id: earlier\n  sequence: []\n- id: invalid\n  when: context.workflow.goal_id != ''\n  sequence: []\n"))
	require.NoError(t, err)
	capture := &captureSubmitter{}
	_, err = StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "t", Cell: "app", CellRepo: "example.test/app", RecipeName: "build"}, capture, *rec)
	require.ErrorContains(t, err, "retired live goal context")
	require.Nil(t, capture.job.Data, "preflight must prevent fresh publication")
}

func TestExactJobQuotedMarkerInvalidRawSubmissionNoWrites(t *testing.T) {
	rec, err := recipe.LoadRecipeFromString([]byte("id: build\nsequence:\n- id: earlier\n  sequence: []\n- id: invalid\n  when: \"${{ context.workflow.goal_id }}\"\n  sequence: []\n"))
	require.NoError(t, err)
	capture := &captureSubmitter{}
	_, err = StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "t", Cell: "app", CellRepo: "example.test/app", RecipeName: "build"}, capture, *rec)
	require.ErrorContains(t, err, "invalid raw CEL expression")
	require.Nil(t, capture.job.Data)
}

type frozenRestartReceipt struct {
	ref      jobref.Ref
	received jobdb.SubmitRestartJobRequest
}

func (s *frozenRestartReceipt) SubmitRestartJobRef(_ context.Context, request jobdb.SubmitRestartJobRequest) (jobref.Ref, error) {
	s.received = request
	return s.ref, nil
}
func TestExactJobRestartReplayRepositoryAssertion(t *testing.T) {
	for _, test := range []struct {
		name, assertion, receipt string
		reject                   bool
	}{
		{"matching", "example.test/app", "example.test/app", false},
		{"normalized", "https://example.test/app.git", "example.test/app", false},
		{"contradictory", "example.test/app", "example.test/other", true},
		{"historical-omission", "", "example.test/app", false},
	} {
		for _, goal := range []string{"", "historical-goal"} {
			t.Run(test.name+"/goal="+goal, func(t *testing.T) {
				request := jobdb.SubmitRestartJobRequest{RequestID: "original-request", Job: jobdb.SubmitRestartJob{
					PriorJobKey: jobdb.JobKey{TenantId: "t", JobId: "prior"}, JobID: "reserved", Cell: "app", CellRepo: test.assertion, ShareGoal: true,
				}}
				frozen, err := json.Marshal(request)
				require.NoError(t, err)
				submitter := &frozenRestartReceipt{ref: jobref.Ref{TenantID: "t", JobID: "reserved", Cell: "app", CellID: "cell", CellRepo: test.receipt, GoalID: goal}}
				_, err = ReplayRestartJob(context.Background(), submitter, request)
				if test.reject {
					require.ErrorContains(t, err, "receipt repository mismatch")
				} else {
					require.NoError(t, err)
				}
				got, marshalErr := json.Marshal(submitter.received)
				require.NoError(t, marshalErr)
				require.Equal(t, frozen, got, "reconciliation preserves original identity, mode and request ID")
			})
		}
	}
}

func TestExactJobQuotedMarkerSubmissionNoWrites(t *testing.T) {
	for _, expr := range []string{
		`context.workflow.goal_id != '{{ literal }}'`,
		`'${{ literal }}' != context['workflow']['goal_id']`,
		`sequence.prior.jobs.goal_ids.size() > 0 && '{{ literal }}' != ''`,
	} {
		t.Run(expr, func(t *testing.T) {
			quoted, err := json.Marshal(expr)
			require.NoError(t, err)
			rec, err := recipe.LoadRecipeFromString([]byte("id: build\nsequence:\n- id: earlier\n  sequence: []\n- id: invalid\n  when: " + string(quoted) + "\n  sequence: []\n"))
			require.NoError(t, err)
			capture := &captureSubmitter{}
			_, err = StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "t", Cell: "app", CellRepo: "example.test/app", RecipeName: "build"}, capture, *rec)
			require.ErrorContains(t, err, "retired live goal context")
			require.Nil(t, capture.job.Data, "preflight must prevent fresh publication")
		})
	}
}
