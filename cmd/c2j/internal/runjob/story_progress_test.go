package runjob

import (
	"bytes"
	"strings"
	"testing"
	"time"

	storylive "github.com/colony-2/c2j/pkg/story/live"
)

func TestStoryRenderersPreserveDisplayTitleAndUseOpFallback(t *testing.T) {
	for _, tt := range []struct {
		title string
		want  string
	}{
		{"run_tests", "run_tests"},
		{"op validation", "op validation"}, // An ID may itself start with a type name.
		{"", "command_execution"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			node := &storylive.JobRunStoryNode{
				ID: "n_1", Kind: storylive.JobRunStoryNodeKindOp,
				Title: tt.title, OpID: "command_execution", Status: storylive.JobRunStoryNodeStatusRunning,
			}
			if got := renderTreeNodeTitle(node); got != tt.want {
				t.Fatalf("tree label = %q, want %q", got, tt.want)
			}
			var out bytes.Buffer
			newLineStoryProgressRenderer(&out, "live").Render(&storylive.JobRunStory{Root: node})
			if got, want := out.String(), "[live] "+tt.want+"\n"; got != want {
				t.Fatalf("line label = %q, want %q", got, want)
			}
		})
	}
}

func TestStoryProgressRenderer_CachedToLiveHandoffDoesNotReprintStablePrefix(t *testing.T) {
	var out bytes.Buffer
	renderer := newStoryProgressRenderer(&out, "cached", false)

	cached := &storylive.JobRunStory{
		Root: &storylive.JobRunStoryNode{
			ID:     "n_1",
			Kind:   storylive.JobRunStoryNodeKindRecipe,
			Title:  "nucleus_test_recipe",
			Status: storylive.JobRunStoryNodeStatusRunning,
			Children: []*storylive.JobRunStoryNode{
				{
					ID:     "n_2",
					Kind:   storylive.JobRunStoryNodeKindSequence,
					Title:  "main",
					Status: storylive.JobRunStoryNodeStatusRunning,
					Children: []*storylive.JobRunStoryNode{
						{
							ID:     "n_3",
							Kind:   storylive.JobRunStoryNodeKindOp,
							Title:  "command_execution",
							Status: storylive.JobRunStoryNodeStatusRunning,
						},
					},
				},
			},
		},
	}
	renderer.Render(cached)
	renderer.SetMode("live")

	live := &storylive.JobRunStory{
		Root: &storylive.JobRunStoryNode{
			ID:     "n_1",
			Kind:   storylive.JobRunStoryNodeKindRecipe,
			Title:  "nucleus_test_recipe",
			Status: storylive.JobRunStoryNodeStatusSucceeded,
			Children: []*storylive.JobRunStoryNode{
				{
					ID:     "n_2",
					Kind:   storylive.JobRunStoryNodeKindSequence,
					Title:  "main",
					Status: storylive.JobRunStoryNodeStatusSucceeded,
					Children: []*storylive.JobRunStoryNode{
						{
							ID:     "n_3",
							Kind:   storylive.JobRunStoryNodeKindOp,
							Title:  "command_execution",
							Status: storylive.JobRunStoryNodeStatusSucceeded,
						},
					},
				},
			},
		},
	}
	renderer.Render(live)

	got := out.String()
	if strings.Count(got, "[cached] nucleus_test_recipe") != 1 {
		t.Fatalf("expected recipe line once, got:\n%s", got)
	}
	if strings.Count(got, "[cached]   main") != 1 || strings.Count(got, "[live]   done main") != 1 {
		t.Fatalf("expected cached sequence enter plus live sequence completion, got:\n%s", got)
	}
	if strings.Count(got, "[cached]     command_execution") != 1 || strings.Count(got, "[live]     done command_execution") != 1 {
		t.Fatalf("expected cached op enter plus live op completion, got:\n%s", got)
	}
	if !strings.Contains(got, "[cached] nucleus_test_recipe") {
		t.Fatalf("expected cached recipe line, got:\n%s", got)
	}
	if !strings.Contains(got, "[live]     done command_execution") {
		t.Fatalf("expected live completion line, got:\n%s", got)
	}
	if !strings.Contains(got, "[live] done nucleus_test_recipe") {
		t.Fatalf("expected live recipe completion line, got:\n%s", got)
	}
	if strings.Contains(got, "[live] nucleus_test_recipe") {
		t.Fatalf("expected live handoff not to reprint recipe line, got:\n%s", got)
	}
}

func TestTreeStoryProgressRenderer_RedrawsSnapshotInPlace(t *testing.T) {
	var out bytes.Buffer
	renderer := newTreeStoryProgressRenderer(&out, "live")

	running := &storylive.JobRunStory{
		JobID:  "job-123",
		Status: storylive.WorkflowStatusRunning,
		Root: &storylive.JobRunStoryNode{
			ID:     "n_1",
			Kind:   storylive.JobRunStoryNodeKindRecipe,
			Title:  "nucleus_test_recipe",
			Status: storylive.JobRunStoryNodeStatusRunning,
			Children: []*storylive.JobRunStoryNode{
				{
					ID:     "n_2",
					Kind:   storylive.JobRunStoryNodeKindOp,
					Title:  "command_execution",
					Status: storylive.JobRunStoryNodeStatusRunning,
				},
			},
		},
	}
	renderer.Render(running)

	completed := &storylive.JobRunStory{
		JobID:  "job-123",
		Status: storylive.WorkflowStatusCompleted,
		Root: &storylive.JobRunStoryNode{
			ID:     "n_1",
			Kind:   storylive.JobRunStoryNodeKindRecipe,
			Title:  "nucleus_test_recipe",
			Status: storylive.JobRunStoryNodeStatusSucceeded,
			Children: []*storylive.JobRunStoryNode{
				{
					ID:     "n_2",
					Kind:   storylive.JobRunStoryNodeKindOp,
					Title:  "command_execution",
					Status: storylive.JobRunStoryNodeStatusSucceeded,
				},
			},
		},
	}
	renderer.Render(completed)

	got := out.String()
	if !strings.Contains(got, "live job job-123 [running]") {
		t.Fatalf("expected running header, got:\n%s", got)
	}
	if !strings.Contains(got, "nucleus_test_recipe [running]") {
		t.Fatalf("expected running recipe line, got:\n%s", got)
	}
	if !strings.Contains(got, "command_execution [succeeded]") {
		t.Fatalf("expected completed op line, got:\n%s", got)
	}
	if !strings.Contains(got, "\x1b[3A\x1b[J") {
		t.Fatalf("expected in-place redraw escape sequence, got:\n%q", got)
	}
}

func TestTreeStoryProgressRenderer_DebouncesNonTerminalUpdatesUntilFlush(t *testing.T) {
	var out bytes.Buffer
	renderer := newTreeStoryProgressRenderer(&out, "live")
	renderer.debounce = time.Hour

	first := &storylive.JobRunStory{
		JobID:  "job-789",
		Status: storylive.WorkflowStatusRunning,
		Root: &storylive.JobRunStoryNode{
			ID:     "n_1",
			Kind:   storylive.JobRunStoryNodeKindRecipe,
			Title:  "test_recipe",
			Status: storylive.JobRunStoryNodeStatusRunning,
		},
	}
	second := &storylive.JobRunStory{
		JobID:  "job-789",
		Status: storylive.WorkflowStatusRunning,
		Root: &storylive.JobRunStoryNode{
			ID:     "n_1",
			Kind:   storylive.JobRunStoryNodeKindRecipe,
			Title:  "test_recipe",
			Status: storylive.JobRunStoryNodeStatusRunning,
			Children: []*storylive.JobRunStoryNode{
				{
					ID:     "n_2",
					Kind:   storylive.JobRunStoryNodeKindOp,
					Title:  "command_execution",
					Status: storylive.JobRunStoryNodeStatusRunning,
				},
			},
		},
	}

	renderer.Render(first)
	before := out.String()
	renderer.Render(second)
	if got := out.String(); got != before {
		t.Fatalf("expected second non-terminal update to be deferred, got:\n%s", got)
	}
	renderer.Flush()
	if !strings.Contains(out.String(), "command_execution [running]") {
		t.Fatalf("expected flushed output to include pending op, got:\n%s", out.String())
	}
}

func TestBuildTreeRenderLines_CollapsesCompletedBranchesAndShowsAttempts(t *testing.T) {
	story := &storylive.JobRunStory{
		JobID:  "job-456",
		Status: storylive.WorkflowStatusRunning,
		Root: &storylive.JobRunStoryNode{
			ID:         "n_1",
			Kind:       storylive.JobRunStoryNodeKindRecipe,
			Title:      "nucleus_test_recipe",
			Status:     storylive.JobRunStoryNodeStatusRunning,
			JobAttempt: 2,
			PastAttempts: []*storylive.JobRunStoryNode{
				{ID: "past_1", Kind: storylive.JobRunStoryNodeKindRecipe, Title: "nucleus_test_recipe", Status: storylive.JobRunStoryNodeStatusFailed},
			},
			Children: []*storylive.JobRunStoryNode{
				{
					ID:      "n_2",
					Kind:    storylive.JobRunStoryNodeKindSequence,
					Title:   "prepare",
					Status:  storylive.JobRunStoryNodeStatusSucceeded,
					Attempt: 2,
					PriorAttempts: []*storylive.JobRunStoryNode{
						{ID: "n_2_a1", Kind: storylive.JobRunStoryNodeKindSequence, Title: "prepare", Status: storylive.JobRunStoryNodeStatusFailed},
					},
					Children: []*storylive.JobRunStoryNode{
						{
							ID:     "n_3",
							Kind:   storylive.JobRunStoryNodeKindOp,
							Title:  "git_clone",
							Status: storylive.JobRunStoryNodeStatusSucceeded,
						},
					},
				},
				{
					ID:     "n_4",
					Kind:   storylive.JobRunStoryNodeKindState,
					Title:  "check_approve",
					Status: storylive.JobRunStoryNodeStatusRunning,
					IsInitial: func() *bool {
						v := true
						return &v
					}(),
				},
			},
		},
	}

	lines := buildTreeRenderLines("live", story)
	got := strings.Join(lines, "\n")

	if !strings.Contains(got, "nucleus_test_recipe (job attempt 2, 1 prior) [running]") {
		t.Fatalf("expected job attempt summary, got:\n%s", got)
	}
	if !strings.Contains(got, "prepare (attempt 2) (1 child collapsed) [succeeded]") {
		t.Fatalf("expected collapsed completed branch with retry summary, got:\n%s", got)
	}
	if strings.Contains(got, "git_clone") {
		t.Fatalf("expected collapsed child op to be hidden, got:\n%s", got)
	}
	if !strings.Contains(got, "check_approve (initial) [running]") {
		t.Fatalf("expected initial state marker, got:\n%s", got)
	}
}
