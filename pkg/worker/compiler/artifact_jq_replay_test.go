package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/swfutil"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestArtifactReviewJQOutputAndReplay(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint("populated=", populated), func(t *testing.T) {
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "A")
			var calls atomic.Int32
			refs := map[string]artifacts.Ref{}
			if populated {
				refs["annotation.txt"] = artifacts.Ref{Kind: artifacts.RefKindValue, Format: "text", Value: json.RawMessage(`" λ\n${{ untouched }}"`)}
			}
			probe := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "review_jq_fixture"}, func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) {
				calls.Add(1)
				return map[string]any{"artifact_refs": refs}, nil
			})
			ctx, eng, _, _ := hookTestEngine(t, workspaceTestWorker(t, probe))
			key := startHookTest(t, ctx, eng, repo, `id: review-jq
sequence:
 - id: review
   sequence:
    - id: request_human_review
      op: review_jq_fixture
   outputs:
    documents: '${{ jq([sequence.request_human_review.outputs.artifact_refs, {"feedback.json": {"kind": "value", "format": "json", "value": {"approved": true} } }], "add") }}'
outputs:
 documents: '${{ sequence.review.outputs.documents }}'
`)
			result, err := swfutil.JobResult(ctx, eng, key)
			require.NoError(t, err)
			var output struct {
				Documents map[string]artifacts.Ref `json:"documents"`
			}
			require.NoError(t, json.Unmarshal(result.GetDataOrPanic(), &output))
			feedback, err := output.Documents["feedback.json"].ValueBytes()
			require.NoError(t, err)
			require.Equal(t, "{\"approved\":true}\n", string(feedback))
			if populated {
				annotation, err := output.Documents["annotation.txt"].ValueBytes()
				require.NoError(t, err)
				require.Equal(t, " λ\n${{ untouched }}", string(annotation))
				require.Equal(t, "annotation.txt", output.Documents["annotation.txt"].Name)
			} else {
				require.Len(t, output.Documents, 1)
			}
			_, err = eng.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			require.NoError(t, err)
			require.EqualValues(t, 1, calls.Load(), "replay must resolve jq outputs without running the task again")
		})
	}
}
