package swfruntime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type chapterErrorRuntime struct {
	jobdb.ConsumerRuntime
	read     func(context.Context, jobdb.ChapterRef) (jobdb.Chapter, error)
	writeErr error
}

func (r chapterErrorRuntime) PutChapter(context.Context, jobdb.PutChapterRequest) error {
	return r.writeErr
}
func (r chapterErrorRuntime) GetChapter(ctx context.Context, ref jobdb.ChapterRef) (jobdb.Chapter, error) {
	return r.read(ctx, ref)
}

func TestC05ChapterVisibilityRetainsErrorsAndCancellation(t *testing.T) {
	ref := jobdb.ChapterRef{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, Ordinal: 2}
	for _, mode := range []string{"write", "read", "cancel", "deadline", "ordinal"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := chapterErrorRuntime{read: func(context.Context, jobdb.ChapterRef) (jobdb.Chapter, error) {
				return jobdb.Chapter{}, jobdb.ErrChapterNotFound
			}}
			expected := error(context.DeadlineExceeded)
			switch mode {
			case "write":
				expected = fmt.Errorf("write: %w", jobdb.ErrExecutionLeaseLost)
				base.writeErr = expected
			case "read":
				expected = fmt.Errorf("read: %w", jobdb.ErrLeasePermissionDenied)
				base.read = func(context.Context, jobdb.ChapterRef) (jobdb.Chapter, error) { return jobdb.Chapter{}, expected }
			case "cancel":
				cancel()
				expected = context.Canceled
			case "ordinal":
				base.read = func(context.Context, jobdb.ChapterRef) (jobdb.Chapter, error) { return jobdb.Chapter{Ordinal: 9}, nil }
			}
			wrapped := &chapterVisibilityRuntime{ConsumerRuntime: base, visibilityTimeout: 20 * time.Millisecond, visibilityPollInterval: time.Millisecond}
			err := wrapped.PutChapter(ctx, jobdb.PutChapterRequest{Ref: ref})
			if mode == "ordinal" {
				require.ErrorContains(t, err, "got ordinal 9, want 2")
			} else {
				require.ErrorIs(t, err, expected)
			}
			if mode == "write" {
				require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
			}
			if mode == "read" {
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
			}
		})
	}
}
