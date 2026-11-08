package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobinspect"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestCancellationMessageFromUnansweredReview(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, reason := range []string{"", "  Use the existing artifacts.\n" + strings.Repeat("保留反馈 ☃\t", 600) + "\n "} {
			name := "without-reason"
			if reason != "" {
				name = "multiline-unicode"
			}
			t.Run(backend+"/"+name, func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				workspaceTestWorker(t, input.GetOp())
				repo := t.TempDir()
				workspaceRepo(t, repo, "source")
				rt := newTimeoutReproRuntime(t, backend)
				child := submitGitLifecycle(t, rt, repo, `id: review-child
version: "1"
input_schema: {}
sequence:
 - id: review
   op: input
   inputs:
     form:
       kind: review
       question: Review the proposed approach
       type: paragraph_text
outputs:
 decision: '${{ sequence.review.outputs.response }}'
`)
				out, err := runGitLifecycle(t, rt, child)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status, "%v", out.JobError)
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				ctl := &workerworkflow.SWFWorkflowControl{Engine: engine}
				reviews, err := input.NewRuntime(ctl, nil)
				require.NoError(t, err)
				form, err := reviews.GetForm(ctx, child.TenantId, child.JobId)
				require.NoError(t, err)
				require.Equal(t, "review", form.Kind)
				require.NotEmpty(t, form.RequestID)
				before, err := ctl.InspectJob(ctx, child)
				require.NoError(t, err)
				require.False(t, before.Terminal)
				require.Nil(t, before.Output)

				parentRecipe := func(mode string) string {
					return fmt.Sprintf(`id: waiting-parent
version: "1"
input_schema: {}
sequence:
 - id: wait
   op: recipe.await_result_soft
   inputs:
     target: {job: %q}
     wait: {on: job, condition: complete}
     return_when: %s
outputs:
 result: '${{ sequence.wait.outputs }}'
`, child.JobId, mode)
				}
				parent := submitGitLifecycle(t, rt, repo, parentRecipe("terminal"))
				out, err = runGitLifecycle(t, rt, parent)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunSuspended, out.Status, "%v", out.JobError)
				waiting, err := rt.GetJob(ctx, parent)
				require.NoError(t, err)
				require.NotNil(t, waiting.BlockingWait)

				require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: child, Reason: reason}))
				stored, err := rt.GetJob(ctx, child)
				require.NoError(t, err)
				require.Equal(t, reason, stored.CompletionDetail)
				for _, mode := range []string{"terminal", "current_status"} {
					key := parent
					if mode == "current_status" {
						key = submitGitLifecycle(t, rt, repo, parentRecipe(mode))
					}
					out, err = runGitLifecycle(t, rt, key)
					require.NoError(t, err)
					raw, err := json.Marshal(gitLifecycleOutput(t, out)["result"])
					require.NoError(t, err)
					var got recipeops.ChildStatusOutput
					require.NoError(t, json.Unmarshal(raw, &got))
					require.Equal(t, "cancelled", got.Status, mode)
					require.True(t, got.Terminal)
					require.Equal(t, "cancellation", got.FailureKind)
					require.Equal(t, reason, got.FailureMessage)
					require.Empty(t, got.Outputs)
				}
				api := &jobinspect.Service{Runtime: rt, Tenant: child.TenantId}
				shown, err := api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: child.JobId})
				require.NoError(t, err)
				require.Equal(t, "CANCELLED", shown.Job.Status)
				require.Equal(t, "cancelled", shown.Job.CompletionStatus)
				require.Equal(t, reason, shown.Job.CompletionDetail)
			})
		}
	}
}
