//go:build integration

package story

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	coretasks "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// This is an integration test to reproduce a production issue where transition evaluation
// expressions show up empty in the JobRunStory REST response even though the recipe contains
// non-empty `when:` expressions.
//
// Run with:
//
//	go test -tags=integration ./internal/story -run TestIntegration_JobRunStoryTransitionExpressions_ArePreserved
func TestIntegration_JobRunStoryTransitionExpressions_ArePreserved(t *testing.T) {
	type in struct{}
	type out struct {
		ExitCode int `json:"exit_code"`
	}

	opType := "test_story_transition_expr_integration"
	coreops.Register(coreops.NewActivityMappedOpV2[in, out](coreops.OpMetadata{Type: opType}, func(_ coreops.OpDependencies, _ context.Context, _ in) (out, error) {
		return out{ExitCode: 0}, nil
	}))

	recipeYAML := `
id: test
version: "1.0.0"
state:
  initial: check_approve
  states:
    check_approve:
      op: ` + opType + `
      inputs: {}
      transitions:
        - to: done
          when: "has(outputs.exit_code) && outputs.exit_code == 0"
        - to: done
          when: "true"
    done:
      op: ` + opType + `
      inputs: {}
outputs: {}
`

	recipeArt := jobdb.NewArtifactFromBytes("test"+starter.RecipeArtifactSuffix, []byte(recipeYAML))
	start := workflowctl.StartJob{RecipeName: "test", GitRef: "main", Inputs: map[string]any{}, JobContext: contextual.JobContext{}}
	jobInput, err := jobdb.NewTaskData(start, recipeArt)
	if err != nil {
		t.Fatalf("NewTaskData(job start): %v", err)
	}

	env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, map[string]any{
		"git":          map[string]any{},
		"nextTaskType": "",
		"output":       map[string]any{"exit_code": 0},
	})
	if err != nil {
		t.Fatalf("NewOutputEnvelope: %v", err)
	}
	raw, _ := json.Marshal(env)
	taskOut := &jobdb.SimpleTaskData{Data: json.RawMessage(raw)}

	engine := &fakeReplayEngine{
		jobInput: jobdb.JobData(jobInput),
		taskScripts: []fakeTaskScript{
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut, Err: nil}}},
			{Attempts: []fakeTaskAttempt{{Attempt: 1, Output: taskOut, Err: nil}}},
		},
	}

	jobKey := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	st, err := BuildJobRunStory(context.Background(), engine, jobKey, nil, nil)
	if err != nil {
		t.Fatalf("BuildJobRunStory: %v", err)
	}

	seq := findFirstKind(st.Root.Children, model.JobRunStoryNodeKindStateMachine)
	if seq == nil {
		t.Fatalf("expected stateMachine under recipe root")
	}
	check := findStateByID(seq.Children, "check_approve")
	if check == nil {
		t.Fatalf("expected check_approve state")
	}
	te := findFirstKind(check.Children, model.JobRunStoryNodeKindTransitionEval)
	if te == nil {
		t.Fatalf("expected transitionEval under check_approve")
	}
	if len(te.Evaluations) < 1 {
		t.Fatalf("expected at least 1 evaluation")
	}

	// Desired behavior: expression should match the recipe text.
	// Current production symptom: expression is "" (empty).
	want := "has(outputs.exit_code) && outputs.exit_code == 0"
	if te.Evaluations[0].Expression != want {
		t.Fatalf("expected evaluation[0].expression=%q, got %q", want, te.Evaluations[0].Expression)
	}
}
