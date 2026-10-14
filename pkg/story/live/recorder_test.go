package live

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

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
