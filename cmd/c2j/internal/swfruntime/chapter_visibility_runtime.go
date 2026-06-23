package swfruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

const (
	defaultChapterVisibilityTimeout      = 5 * time.Second
	defaultChapterVisibilityPollInterval = 10 * time.Millisecond
)

type chapterVisibilityRuntime struct {
	jobdb.WorkflowRuntime

	visibilityTimeout      time.Duration
	visibilityPollInterval time.Duration
}

func withChapterVisibility(runtime jobdb.WorkflowRuntime) jobdb.WorkflowRuntime {
	if runtime == nil {
		return nil
	}
	return &chapterVisibilityRuntime{WorkflowRuntime: runtime}
}

func (r *chapterVisibilityRuntime) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if err := r.WorkflowRuntime.PutChapter(ctx, req); err != nil {
		return err
	}
	return r.awaitChapterVisible(ctx, req.Ref)
}

func (r *chapterVisibilityRuntime) awaitChapterVisible(ctx context.Context, ref jobdb.ChapterRef) error {
	waitCtx := ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}

	timeout := r.visibilityTimeout
	if timeout <= 0 {
		timeout = defaultChapterVisibilityTimeout
	}
	deadline := time.Now().Add(timeout)
	if existingDeadline, ok := waitCtx.Deadline(); !ok || existingDeadline.After(deadline) {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithDeadline(waitCtx, deadline)
		defer cancel()
	}

	pollInterval := r.visibilityPollInterval
	if pollInterval <= 0 {
		pollInterval = defaultChapterVisibilityPollInterval
	}

	for {
		chapter, err := r.WorkflowRuntime.GetChapter(waitCtx, ref)
		if err == nil {
			if chapter.Ordinal != ref.Ordinal {
				return fmt.Errorf("confirm chapter visibility: got ordinal %d, want %d", chapter.Ordinal, ref.Ordinal)
			}
			return nil
		}
		if !errors.Is(err, jobdb.ErrChapterNotFound) {
			return fmt.Errorf("confirm chapter %d visibility: %w", ref.Ordinal, err)
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return fmt.Errorf("chapter ordinal %d was not visible after write: %w", ref.Ordinal, waitCtx.Err())
		case <-timer.C:
		}
	}
}
