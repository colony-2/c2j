package swfruntime

import (
	"context"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/swf-go/pkg/swf"
)

func TestOpenEmbedPersistsJobsAcrossReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	t.Setenv(defaults.EmbedRootEnv, t.TempDir())

	handle, err := Open(ctx, "embed:///")
	if err != nil {
		t.Fatalf("Open(embed): %v", err)
	}

	data := swf.NewTaskDataOrPanic(map[string]any{"job_id": "job-1"})
	jobKey, err := handle.Engine.SubmitJob(ctx, swf.SubmitJob{
		TenantId:  "tenant-embed-test",
		JobID:     "job-1",
		JobType:   "alpha",
		Data:      data,
		RunPolicy: swf.DefaultRunPolicy(),
	})
	cleanupErr := handle.Cleanup()
	if err != nil {
		t.Fatalf("SubmitJob(): %v", err)
	}
	if cleanupErr != nil {
		t.Fatalf("Cleanup(): %v", cleanupErr)
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
	if info.Status != swf.JobStatusReady {
		t.Fatalf("job status = %s, want %s", info.Status, swf.JobStatusReady)
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
