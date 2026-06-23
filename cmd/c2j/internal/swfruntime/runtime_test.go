package swfruntime

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func TestOpenEmbedPersistsJobsAcrossReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	root := t.TempDir()
	t.Setenv(defaults.EmbedRootEnv, root)

	handle, err := Open(ctx, "embed:///")
	if err != nil {
		t.Fatalf("Open(embed): %v", err)
	}

	data := jobdb.NewTaskDataOrPanic(map[string]any{"job_id": "job-1"})
	jobKey, err := handle.Engine.SubmitJob(ctx, jobdb.SubmitJob{
		TenantId:  "tenant-embed-test",
		JobID:     "job-1",
		JobType:   "alpha",
		Data:      data,
		RunPolicy: jobdb.DefaultRunPolicy(),
	})
	cleanupErr := handle.Cleanup()
	if err != nil {
		t.Fatalf("SubmitJob(): %v", err)
	}
	if cleanupErr != nil {
		t.Fatalf("Cleanup(): %v", cleanupErr)
	}
	if info, err := os.Stat(filepath.Join(root, "swf.db")); err != nil || info.IsDir() {
		t.Fatalf("expected SQLite db file under embed root, stat err=%v, is_dir=%v", err, err == nil && info.IsDir())
	}
	if info, err := os.Stat(filepath.Join(root, "swf.db.blobs")); err != nil || !info.IsDir() {
		t.Fatalf("expected SQLite blob dir under embed root, stat err=%v, is_dir=%v", err, err == nil && info.IsDir())
	}

	reopened, err := Open(ctx, "embed:///")
	if err != nil {
		t.Fatalf("Open(embed) reopen: %v", err)
	}
	defer reopened.Cleanup()

	info, err := reopened.Engine.GetJob(ctx, jobKey)
	if err != nil {
		t.Fatalf("GetJob(): %v", err)
	}
	if info.Status != jobdb.JobStatusReady {
		t.Fatalf("job status = %s, want %s", info.Status, jobdb.JobStatusReady)
	}
}

func TestOpenEmbedRejectsConcurrentOpenOnSameRoot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Setenv(defaults.EmbedRootEnv, t.TempDir())

	handle, err := Open(ctx, defaults.EmbedURL)
	if err != nil {
		t.Fatalf("Open(embed): %v", err)
	}
	defer handle.Cleanup()

	other, err := Open(ctx, defaults.EmbedURL)
	if err == nil {
		defer other.Cleanup()
		t.Fatal("expected second Open(embed) to fail while lock is held")
	}
}

func TestChapterVisibilityRuntimeWaitsUntilWrittenChapterCanBeRead(t *testing.T) {
	ref := jobdb.ChapterRef{
		JobKey:  jobdb.JobKey{TenantId: "tenant", JobId: "job"},
		Ordinal: 2,
	}
	chapter := jobdb.Chapter{
		Ordinal:  ref.Ordinal,
		TaskType: "task",
		Body: jobdb.TaskAttemptOutcomeChapter{
			Outcome: jobdb.ApplicationOutputOutcome{
				Output: jobdb.ApplicationOutputBytes{Data: []byte(`{"ok":true}`)},
			},
		},
	}
	underlying := &delayedChapterRuntime{
		visibleAfterGetCalls: 3,
		chapters:             map[jobdb.ChapterRef]jobdb.Chapter{},
	}
	runtime := &chapterVisibilityRuntime{
		WorkflowRuntime:        underlying,
		visibilityTimeout:      time.Second,
		visibilityPollInterval: time.Millisecond,
	}

	err := runtime.PutChapter(context.Background(), jobdb.PutChapterRequest{
		Ref:     ref,
		Chapter: chapter,
	})
	if err != nil {
		t.Fatalf("PutChapter(): %v", err)
	}
	if got := underlying.getCalls(); got != 3 {
		t.Fatalf("GetChapter calls = %d, want 3", got)
	}
}

type delayedChapterRuntime struct {
	jobdb.WorkflowRuntime

	mu                   sync.Mutex
	visibleAfterGetCalls int
	getChapterCalls      int
	chapters             map[jobdb.ChapterRef]jobdb.Chapter
}

func (r *delayedChapterRuntime) PutChapter(_ context.Context, req jobdb.PutChapterRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chapters[req.Ref] = req.Chapter
	return nil
}

func (r *delayedChapterRuntime) GetChapter(_ context.Context, ref jobdb.ChapterRef) (jobdb.Chapter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getChapterCalls++
	if r.getChapterCalls < r.visibleAfterGetCalls {
		return jobdb.Chapter{}, jobdb.ErrChapterNotFound
	}
	chapter, ok := r.chapters[ref]
	if !ok {
		return jobdb.Chapter{}, jobdb.ErrChapterNotFound
	}
	return chapter, nil
}

func (r *delayedChapterRuntime) getCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getChapterCalls
}
