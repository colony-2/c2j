package live

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/swf-go/pkg/swf"
)

func TestRecorderSnapshot_DoesNotDuplicateSyntheticSourceResolutionNode(t *testing.T) {
	jobKey := swf.JobKey{TenantId: "tenant", JobId: "job"}
	rec := NewRecorder(Options{JobKey: jobKey})

	rec.OnJobStart(swf.JobStartEvent{
		JobKey:        jobKey,
		AttemptNumber: 1,
		At:            time.Unix(10, 0).UTC(),
	})
	rec.OnRecipeLoaded("story-ref")
	rec.OnTaskStart(swf.TaskStartEvent{
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

	rec.OnTaskEnd(swf.TaskEndEvent{
		JobKey:        jobKey,
		TaskType:      compiler.RootSourceResolutionTaskType,
		Ordinal:       1,
		AttemptNumber: 1,
		Output:        &swf.SimpleTaskData{Data: raw},
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
