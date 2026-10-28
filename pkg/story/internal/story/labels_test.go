package story

import (
	"context"
	"testing"

	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	coretasks "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestStoryLabelsPreferIDsAtEveryDepth(t *testing.T) {
	const opType = "story_label_probe"
	coreops.Register(coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: opType}, func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) {
		panic("story inspection must not execute operations")
	}))
	const recipeYAML = `id: label_recipe
sequence:
  - id: outer
    sequence:
      - id: run_tests
        op: story_label_probe
      - sequence:
          - op: story_label_probe
      - id: review
        state:
          initial: checking
          states:
            checking:
              sequence:
                - id: check_result
                  op: story_label_probe
  - state:
      initial: done
      states:
        done:
          op: story_label_probe
`
	input := jobdb.NewTaskDataOrPanic(workflowctl.StartJob{RecipeName: "labels"}, jobdb.NewArtifactFromBytes("labels"+starter.RecipeArtifactSuffix, []byte(recipeYAML)))
	env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git": map[string]any{}, "nextTaskType": "", "output": map[string]any{},
	})
	require.NoError(t, err)
	engine := &fakeReplayEngine{jobInput: input}
	for i := 0; i < 4; i++ {
		engine.taskScripts = append(engine.taskScripts, fakeTaskScript{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: jobdb.NewTaskDataOrPanic(env)}}})
	}
	st, err := BuildJobRunStory(context.Background(), engine, jobdb.JobKey{TenantId: "tenant", JobId: "labels"}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, model.WorkflowStatusCompleted, st.Status)
	var labels []string
	var visit func(*model.JobRunStoryNode)
	visit = func(n *model.JobRunStoryNode) {
		labels = append(labels, string(n.Kind)+":"+n.Title)
		if n.Kind == model.JobRunStoryNodeKindOp {
			require.Equal(t, opType, n.OpID, "display labels must not replace operation identity")
		}
		for _, child := range n.Children {
			visit(child)
		}
	}
	visit(st.Root)
	require.Equal(t, []string{
		"recipe:label_recipe", "sequence:label_recipe", "sequence:outer",
		"op:run_tests", "sequence:sequence", "op:story_label_probe",
		"stateMachine:review", "state:checking", "sequence:sequence", "op:check_result",
		"stateMachine:state_machine", "state:done", "op:story_label_probe",
	}, labels)
}
