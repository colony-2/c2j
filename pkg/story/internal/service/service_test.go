package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/strata-go/pkg/client/story"
)

func TestMapWorkflowStatus(t *testing.T) {
	if got := mapWorkflowStatus(jobdb.JobStatusActive); got != "running" {
		t.Fatalf("expected running, got %q", got)
	}
	if got := mapWorkflowStatus(jobdb.JobStatusCompleted); got != "completed" {
		t.Fatalf("expected completed, got %q", got)
	}
	if got := mapWorkflowStatus(jobdb.JobStatusExpired); got != "timed_out" {
		t.Fatalf("expected timed_out, got %q", got)
	}
	if got := mapWorkflowStatus(jobdb.JobStatusCrashConcern); got != "failed" {
		t.Fatalf("expected failed, got %q", got)
	}
	if got := mapWorkflowStatus(jobdb.JobStatus("UNKNOWN")); got != "unknown" {
		t.Fatalf("expected unknown, got %q", got)
	}
}

func TestChapterToDetailInputOutputAndError(t *testing.T) {
	startedAt := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	makeChapter := func(ordinal int64, payloadKind string, payload map[string]interface{}, taskType string) story.Chapter {
		env := chapterEnvelope{
			Meta: chapterMeta{
				Ordinal:   ordinal,
				TaskType:  taskType,
				CreatedAt: startedAt,
			},
			PayloadKind: payloadKind,
		}
		if payload != nil {
			raw, _ := json.Marshal(payload)
			env.Payload = raw
		}
		rawEnv, _ := json.Marshal(env)
		return story.NewChapter().WithBytes(rawEnv).WithOrdinal(ordinal)
	}

	chapter0 := makeChapter(0, "App", map[string]interface{}{"input": "value"}, "op")
	detail0, err := chapterToDetail(chapter0)
	if err != nil {
		t.Fatalf("chapterToDetail input: %v", err)
	}
	if detail0.Input["input"] != "value" {
		t.Fatalf("expected input payload, got %#v", detail0.Input)
	}
	if detail0.Output != nil {
		t.Fatalf("expected output nil for ordinal 0, got %#v", detail0.Output)
	}

	chapter1 := makeChapter(1, "App", map[string]interface{}{"output": "value"}, "task")
	detail1, err := chapterToDetail(chapter1)
	if err != nil {
		t.Fatalf("chapterToDetail output: %v", err)
	}
	if detail1.Output == nil || (*detail1.Output)["output"] != "value" {
		t.Fatalf("expected output payload, got %#v", detail1.Output)
	}
	if detail1.Input == nil || len(detail1.Input) != 0 {
		t.Fatalf("expected empty input for ordinal > 0, got %#v", detail1.Input)
	}

	chapterErr := makeChapter(2, "AppError", map[string]interface{}{"message": "boom"}, "")
	detailErr, err := chapterToDetail(chapterErr)
	if err != nil {
		t.Fatalf("chapterToDetail error: %v", err)
	}
	if detailErr.Error == nil || *detailErr.Error != "boom" {
		t.Fatalf("expected error message boom, got %#v", detailErr.Error)
	}
	if detailErr.ChapterType != "workflow" {
		t.Fatalf("expected default chapter type workflow, got %q", detailErr.ChapterType)
	}
}

func TestWorkflowStatusesToJobStatuses_NilReturnsNil(t *testing.T) {
	// Test with nil statuses - should return nil to indicate "all statuses"
	// The SWF engine will interpret nil as "no filter"
	result := workflowStatusesToJobStatuses(nil)

	if result != nil {
		t.Fatalf("expected nil when input is nil, got %v (len=%d)", result, len(result))
	}
}

func TestWorkflowStatusesToJobStatuses_EmptyReturnsNil(t *testing.T) {
	// Test with empty slice - should return nil to indicate "all statuses"
	// The SWF engine will interpret nil as "no filter"
	result := workflowStatusesToJobStatuses([]model.WorkflowStatus{})

	if result != nil {
		t.Fatalf("expected nil when input is empty, got %v (len=%d)", result, len(result))
	}
}

func TestWorkflowStatusesToJobStatuses_SpecificStatus(t *testing.T) {
	// Test with specific status - should only return those job statuses
	result := workflowStatusesToJobStatuses([]model.WorkflowStatus{model.WorkflowStatusCompleted})

	if len(result) != 1 {
		t.Fatalf("expected 1 job status for 'completed', got %d", len(result))
	}

	if result[0] != jobdb.JobStatusCompleted {
		t.Errorf("expected JobStatusCompleted, got %q", result[0])
	}
}

func TestBuildSummary_UsesJobMetadata(t *testing.T) {
	created := time.Date(2026, 2, 5, 12, 1, 0, 0, time.UTC)

	meta := starter.JobMetadata{
		Version:    starter.JobMetadataVersion,
		RecipeName: "recipe-1",
		CellID:     "cell-1",
		CellName:   "alpha",
		GitRef:     "main",
	}
	metaRaw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}

	svc := &Service{logger: slog.Default()}
	job := jobdb.JobSummary{
		JobKey:    jobdb.JobKey{TenantId: "proj-1", JobId: "job-1"},
		Status:    jobdb.JobStatusActive,
		CreatedAt: created,
		Metadata:  metaRaw,
	}

	summary, ok, err := svc.buildSummary(context.Background(), "proj-1", job)
	if err != nil {
		t.Fatalf("buildSummary error: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if summary.RecipeName != "recipe-1" {
		t.Fatalf("expected recipe_name recipe-1, got %q", summary.RecipeName)
	}
	if summary.CellID == nil || *summary.CellID != "cell-1" {
		t.Fatalf("expected cell_id cell-1, got %#v", summary.CellID)
	}
	if summary.CellName == nil || *summary.CellName != "alpha" {
		t.Fatalf("expected cell_name alpha, got %#v", summary.CellName)
	}
	if summary.SubmittedAt == nil || !summary.SubmittedAt.Equal(created) {
		t.Fatalf("expected submitted_at %v, got %#v", created, summary.SubmittedAt)
	}
	if summary.Actor.Type != model.ActorTypeUser || summary.Actor.User != nil {
		t.Fatalf("expected actor type user without user details, got %#v", summary.Actor)
	}
}
