package compiler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/colony-2/swf-go/pkg/swf"
)

func TestLogReplayCacheMissUsesDebug(t *testing.T) {
	handler := &recordingSlogHandler{}
	logger := slog.New(handler)
	err := swf.ReplayCacheMissError{
		TaskType: "example_task",
		Ordinal:  1,
		Attempt:  1,
		Reason:   swf.ReplayCacheMissTaskResultMissing,
	}

	if !logReplayCacheMiss(logger, "cache miss", err, "op", "example") {
		t.Fatal("expected replay cache miss to be handled")
	}

	if len(handler.records) != 1 {
		t.Fatalf("expected one log record, got %d", len(handler.records))
	}
	if got := handler.records[0].Level; got != slog.LevelDebug {
		t.Fatalf("log level = %s, want debug", got)
	}
	if got := handler.records[0].Message; got != "cache miss" {
		t.Fatalf("message = %q, want cache miss", got)
	}
}

func TestLogReplayCacheMissHandlesWrappedError(t *testing.T) {
	handler := &recordingSlogHandler{}
	logger := slog.New(handler)
	err := swf.ReplayCacheMissError{
		TaskType: "example_task",
		Ordinal:  1,
		Attempt:  1,
		Reason:   swf.ReplayCacheMissTaskResultMissing,
	}

	if !logReplayCacheMiss(logger, "cache miss", fmt.Errorf("outer: %w", err)) {
		t.Fatal("wrapped replay cache miss should still be handled")
	}
}

func TestLogReplayCacheMissIgnoresOrdinaryErrors(t *testing.T) {
	handler := &recordingSlogHandler{}
	logger := slog.New(handler)

	if logReplayCacheMiss(logger, "cache miss", errors.New("boom")) {
		t.Fatal("ordinary error should not be handled as replay cache miss")
	}
	if len(handler.records) != 0 {
		t.Fatalf("expected no log records, got %d", len(handler.records))
	}
}

type recordingSlogHandler struct {
	records []slog.Record
}

func (h *recordingSlogHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *recordingSlogHandler) Handle(_ context.Context, record slog.Record) error {
	h.records = append(h.records, record.Clone())
	return nil
}

func (h *recordingSlogHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *recordingSlogHandler) WithGroup(string) slog.Handler {
	return h
}
