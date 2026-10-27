package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	coretasks "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestTaskStartProjectsInputAndClearsPreviousAttempt(t *testing.T) {
	states := []struct {
		state jobworkflow.TaskState
		want  JobRunStoryNodeStatus
	}{
		{jobworkflow.TaskStateRunning, JobRunStoryNodeStatusRunning},
		{jobworkflow.TaskStateWaiting, JobRunStoryNodeStatusPending},
		{jobworkflow.TaskStateReady, JobRunStoryNodeStatusPending},
		{jobworkflow.TaskStateCancelled, JobRunStoryNodeStatusCanceled},
		{jobworkflow.TaskState("UNKNOWN"), JobRunStoryNodeStatusUnknown},
		{jobworkflow.TaskState("future-state"), JobRunStoryNodeStatusUnknown},
		{jobworkflow.TaskState(""), JobRunStoryNodeStatusUnknown},
	}
	for _, kind := range []string{"operation", "source-resolution"} {
		t.Run(kind, func(t *testing.T) {
			for _, tt := range states {
				t.Run(string(tt.state), func(t *testing.T) {
					r := NewRecorder(Options{})
					r.OnJobStart(jobworkflow.JobStartEvent{AttemptNumber: 1})
					tree := r.ensureAttemptTree()
					op := tree.newNode(JobRunStoryNodeKindOp, "op work")
					op.OpID = "work"
					r.SetCurrentOpNode(op)
					taskType := "work:work"
					wantInput := map[string]any{"value": "resolved"}
					input := jobdb.NewTaskDataOrPanic(ops.ActivityInvocationRequest{Input: wantInput})
					node := func() *JobRunStoryNode { require.Len(t, op.Children, 1); return op.Children[0] }
					if kind == "source-resolution" {
						taskType = compiler.RootSourceResolutionTaskType
						wantInput = map[string]any{"project_id": "tenant", "selector": "recipe@v1", "lookup_repo": "https://example.test/recipes", "lookup_ref": "main"}
						input = jobdb.NewTaskDataOrPanic(wantInput)
						node = func() *JobRunStoryNode { return r.sourceNodes[1].node }
					}
					started, finished := time.Unix(10, 0), time.Unix(11, 0)
					var oldOutput any = "old output"
					oldData := jobdb.NewTaskDataOrPanic(oldOutput, jobdb.NewArtifactFromBytes("old.txt", []byte("old")))
					if kind == "operation" {
						oldOutput = map[string]any{"value": "old output"}
						env, err := coretasks.NewOutputEnvelope(coretasks.OutputKindActivityInvocationOutput, ops.ActivityInvocationOutput{
							OpOutput:     oldOutput.(map[string]any),
							ArtifactRefs: map[string]recipeartifacts.Ref{"diagnostic": recipeartifacts.NewExternalRef("diagnostic", "https://example.test/old.txt", false)},
						})
						require.NoError(t, err)
						oldData = jobdb.NewTaskDataOrPanic(env, jobdb.NewArtifactFromBytes("old.txt", []byte("old")))
					}
					r.OnTaskStart(jobworkflow.TaskStartEvent{InvocationID: "work/attempt:1", TaskType: taskType, AttemptNumber: 1, Input: input, At: started})
					// A completed event's outcome, rather than an unfinished state, is authoritative.
					r.OnTaskEnd(jobworkflow.TaskEndEvent{InvocationID: "work/attempt:1", TaskType: taskType, AttemptNumber: 1, Output: oldData, Err: errors.New("old failure"), At: finished})
					id := node().ID
					require.Equal(t, JobRunStoryNodeStatusFailed, node().Status)
					require.NotNil(t, node().Error)
					r.OnTaskStart(jobworkflow.TaskStartEvent{Unfinished: true, State: tt.state, InvocationID: "work/attempt:2", TaskType: taskType, Ordinal: 42, AttemptNumber: 2, Input: input})
					n := node()
					require.Equal(t, id, n.ID)
					require.Equal(t, "work", n.InvocationID)
					require.Equal(t, int64(42), *n.TaskOrdinal)
					require.Equal(t, 2, n.Attempt)
					require.Equal(t, wantInput, n.Input)
					require.Equal(t, tt.want, n.Status)
					require.Nil(t, n.Output)
					require.Empty(t, n.ArtifactKeys)
					require.Empty(t, n.ArtifactRefs)
					require.Nil(t, n.StartedAt, "unknown start time must not inherit the previous attempt")
					require.Nil(t, n.FinishedAt)
					require.Nil(t, n.Error)
					require.Len(t, n.PriorAttempts, 1)
					prior := n.PriorAttempts[0]
					require.Equal(t, oldOutput, prior.Output)
					require.Len(t, prior.ArtifactKeys, 1)
					if kind == "operation" {
						require.Len(t, prior.ArtifactRefs, 1)
					}
					require.Equal(t, "old failure", prior.Error.Message)
					require.Equal(t, started, *prior.StartedAt)
					require.Equal(t, finished, *prior.FinishedAt)
				})
			}
		})
	}
}

func TestRecordedTaskStartIgnoresUnfinishedState(t *testing.T) {
	r := NewRecorder(Options{})
	r.OnJobStart(jobworkflow.JobStartEvent{AttemptNumber: 1})
	r.OnTaskStart(jobworkflow.TaskStartEvent{InvocationID: "source/attempt:1", TaskType: compiler.RootSourceResolutionTaskType, AttemptNumber: 1, State: jobworkflow.TaskStateCancelled})
	require.Equal(t, JobRunStoryNodeStatusRunning, r.sourceNodes[1].node.Status)
	r.OnTaskEnd(jobworkflow.TaskEndEvent{InvocationID: "source/attempt:1", TaskType: compiler.RootSourceResolutionTaskType, AttemptNumber: 1})
	require.Equal(t, JobRunStoryNodeStatusSucceeded, r.sourceNodes[1].node.Status)
}

func TestReplayMissPreservesObservedUnfinishedInvocation(t *testing.T) {
	r := NewRecorder(Options{})
	r.OnJobStart(jobworkflow.JobStartEvent{AttemptNumber: 1})
	tree := r.ensureAttemptTree()
	op := tree.newNode(JobRunStoryNodeKindOp, "op work")
	op.OpID = "work"
	r.SetCurrentOpNode(op)
	input := jobdb.NewTaskDataOrPanic(ops.ActivityInvocationRequest{Input: map[string]any{"value": "resolved"}})
	r.OnTaskStart(jobworkflow.TaskStartEvent{InvocationID: "work/attempt:2", TaskType: "work:work", AttemptNumber: 2, Ordinal: 12, Unfinished: true, State: jobworkflow.TaskStateWaiting, Input: input})
	exec := newRecordingExecutor(compiler.DefaultRecipeExecutor{}, tree, r)
	// A physical ordinal is not the invocation identity.
	exec.ensureReplayMissStepNode(op, "work", jobworkflow.ReplayCacheMissError{InvocationID: "work/attempt:2", TaskType: "work:work", Attempt: 2, Ordinal: 99, Reason: jobworkflow.ReplayCacheMissTaskResultMissing, State: jobworkflow.TaskStateWaiting})
	require.Len(t, op.Children, 1)
	require.Equal(t, JobRunStoryNodeStatusPending, op.Children[0].Status)
	require.Equal(t, map[string]any{"value": "resolved"}, op.Children[0].Input)
	require.Nil(t, op.Children[0].Error)
	require.Nil(t, op.Children[0].Output)
}

func TestUnfinishedContainerStatusAndExecutionErrors(t *testing.T) {
	for _, tt := range []struct {
		state jobworkflow.TaskState
		want  JobRunStoryNodeStatus
	}{
		{jobworkflow.TaskStateWaiting, JobRunStoryNodeStatusPending},
		{jobworkflow.TaskStateReady, JobRunStoryNodeStatusPending},
		{jobworkflow.TaskStateRunning, JobRunStoryNodeStatusRunning},
		{jobworkflow.TaskState("UNKNOWN"), JobRunStoryNodeStatusUnknown},
	} {
		t.Run(string(tt.state), func(t *testing.T) {
			children := []*JobRunStoryNode{{Status: JobRunStoryNodeStatusSucceeded}, {Status: tt.want}}
			require.Equal(t, tt.want, deriveContainerStatus(children))
			miss := fmt.Errorf("operation: %w", jobworkflow.ReplayCacheMissError{State: tt.state})
			require.Equal(t, tt.want, statusFromErr(miss, JobRunStoryNodeStatusRunning))
			require.Equal(t, JobRunStoryNodeStatusFailed, statusFromErr(errors.New("execution failed"), tt.want))
		})
	}
}

func TestRecorderSnapshot_DoesNotDuplicateSyntheticSourceResolutionNode(t *testing.T) {
	jobKey := jobdb.JobKey{TenantId: "tenant", JobId: "job"}
	rec := NewRecorder(Options{JobKey: jobKey})

	rec.OnJobStart(jobworkflow.JobStartEvent{
		JobKey:        jobKey,
		AttemptNumber: 1,
		At:            time.Unix(10, 0).UTC(),
	})
	rec.OnRecipeLoaded("story-ref")
	rec.OnTaskStart(jobworkflow.TaskStartEvent{
		JobKey:        jobKey,
		TaskType:      compiler.RootSourceResolutionTaskType,
		Ordinal:       1,
		AttemptNumber: 1,
		At:            time.Unix(11, 0).UTC(),
	})

	resolvedSource := compiler.ResolvedRecipeSource{
		RecipeSourceResolution: compiler.RecipeSourceResolution{
			SourceKind:        compiler.RecipeSourceKindGit,
			SubmittedSelector: "refs/heads/main",
			ResolvedSelector:  "refs/heads/main",
			ResolvedCommit:    "abc123",
		},
		RecipeYAML: "id: story_ref_recipe\nsequence: []\n",
	}
	raw, err := json.Marshal(resolvedSource)
	if err != nil {
		t.Fatalf("marshal resolved source: %v", err)
	}

	rec.OnTaskEnd(jobworkflow.TaskEndEvent{
		JobKey:        jobKey,
		TaskType:      compiler.RootSourceResolutionTaskType,
		Ordinal:       1,
		AttemptNumber: 1,
		Output:        &jobdb.SimpleTaskData{Data: raw},
		At:            time.Unix(12, 0).UTC(),
	})

	first := rec.Snapshot()
	second := rec.Snapshot()

	if first == nil || first.Root == nil {
		t.Fatalf("expected first snapshot root, got %#v", first)
	}
	if second == nil || second.Root == nil {
		t.Fatalf("expected second snapshot root, got %#v", second)
	}
	if got := len(first.Root.Children); got != 1 {
		t.Fatalf("expected first snapshot to have 1 child, got %d", got)
	}
	if got := len(second.Root.Children); got != 1 {
		t.Fatalf("expected second snapshot to have 1 child, got %d", got)
	}
	if first.Root.Children[0].Kind != JobRunStoryNodeKindRecipeSourceResolution {
		t.Fatalf("expected first child kind %q, got %q", JobRunStoryNodeKindRecipeSourceResolution, first.Root.Children[0].Kind)
	}
	if second.Root.Children[0].Kind != JobRunStoryNodeKindRecipeSourceResolution {
		t.Fatalf("expected second child kind %q, got %q", JobRunStoryNodeKindRecipeSourceResolution, second.Root.Children[0].Kind)
	}
	if first.Recipe.Source.ResolutionTaskOrdinal == nil || *first.Recipe.Source.ResolutionTaskOrdinal != 1 {
		t.Fatalf("expected first resolution ordinal 1, got %#v", first.Recipe.Source.ResolutionTaskOrdinal)
	}
	if second.Recipe.Source.ResolutionTaskOrdinal == nil || *second.Recipe.Source.ResolutionTaskOrdinal != 1 {
		t.Fatalf("expected second resolution ordinal 1, got %#v", second.Recipe.Source.ResolutionTaskOrdinal)
	}
}

func TestCloseNotificationsFencesLateCallbacks(t *testing.T) {
	var calls atomic.Int32
	rec := NewRecorder(Options{OnChange: func(*JobRunStory) { calls.Add(1) }})
	rec.notify()
	release, done := make(chan struct{}), make(chan struct{})
	go func() { <-release; rec.notify(); close(done) }()
	rec.CloseNotifications()
	close(release)
	<-done
	rec.CloseNotifications()
	if calls.Load() != 1 {
		t.Fatalf("late notifications: %d", calls.Load())
	}
}

// JobDB may report task outcomes while the orchestration callback is still
// updating its recipe tree. Snapshots must be isolated from both writers.
func TestRecorderSnapshotDuringStateUpdates(t *testing.T) {
	rec := NewRecorder(Options{})
	tree := rec.ensureAttemptTree()
	root := tree.newNode(JobRunStoryNodeKindRecipe, "recipe")
	tree.push("root", root)
	rec.SetRoot(root)
	exec := newRecordingExecutor(compiler.DefaultRecipeExecutor{}, tree, rec)
	observer := newStoryStateObserver(tree, rec)

	start, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		<-start
		for range 200 {
			observer.StateEntered("work")
			exec.VarsResolved("", "", map[string]interface{}{"value": "rendered"})
			observer.TransitionEvalauted("true", true, "next")
			observer.TransitionSelected("work", "next", map[string]interface{}{"value": "next"})
			observer.StateExited("work")
			observer.Flush()
		}
	}()
	close(start)
	for range 200 {
		if snapshot := rec.Snapshot(); snapshot.Root == nil {
			t.Fatal("missing recipe root")
		}
	}
	<-done
	snapshot := rec.Finalize(nil)
	if len(snapshot.Root.Children) != 200 {
		t.Fatalf("states recorded: %d", len(snapshot.Root.Children))
	}
	for _, state := range snapshot.Root.Children {
		if state.Status != JobRunStoryNodeStatusSucceeded || len(state.Children) != 1 || state.RenderedVars["value"] != "rendered" {
			t.Fatalf("incomplete state: %#v", state)
		}
		if decision := state.Children[0].Decision; decision == nil || decision.Payload["value"] != "next" {
			t.Fatalf("missing transition payload: %#v", decision)
		}
	}
}
