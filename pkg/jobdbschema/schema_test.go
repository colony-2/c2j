package jobdbschema

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
)

func TestSchemaRegistersWithJobDB(t *testing.T) {
	ctx := context.Background()
	runtime := toyruntime.New()

	info, err := runtime.RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{
		TenantId: "tenant",
		Schema:   Schema(),
	})
	if err != nil {
		t.Fatalf("RegisterJobSchema: %v", err)
	}

	wantHash, err := Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if info.SchemaHash != wantHash {
		t.Fatalf("schema hash = %q, want %q", info.SchemaHash, wantHash)
	}
}

func TestSubmitterRegistersOnMissingSchemaHash(t *testing.T) {
	ctx := context.Background()
	submitter := &missingSchemaSubmitter{}
	registry := &captureRegistry{}

	key, err := (Submitter{
		JobSubmitter: submitter,
		Registry:     registry,
	}).SubmitJob(ctx, jobdb.SubmitJob{
		TenantId: "tenant",
		JobType:  "recipe",
		JobID:    "job-1",
	})
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	if key != (jobdb.JobKey{TenantId: "tenant", JobId: "job-1"}) {
		t.Fatalf("key = %#v", key)
	}
	if submitter.calls != 2 {
		t.Fatalf("submit calls = %d, want 2", submitter.calls)
	}
	if registry.calls != 1 {
		t.Fatalf("register calls = %d, want 1", registry.calls)
	}

	wantHash, err := Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if submitter.last.Schema == nil {
		t.Fatalf("schema selector was not set")
	}
	if submitter.last.Schema.Hash != wantHash {
		t.Fatalf("schema hash = %q, want %q", submitter.last.Schema.Hash, wantHash)
	}
	if len(submitter.last.Schema.Schema) != 0 {
		t.Fatalf("expected hash-only selector, got inline schema")
	}
	if registry.last.TenantId != "tenant" {
		t.Fatalf("registered tenant = %q, want tenant", registry.last.TenantId)
	}
}

func TestRestartSubmitterRegistersOnMissingSchemaHash(t *testing.T) {
	ctx := context.Background()
	submitter := &missingSchemaRestartSubmitter{}
	registry := &captureRegistry{}

	key, err := (Submitter{
		RestartSubmitter: submitter,
		Registry:         registry,
	}).SubmitRestartJob(ctx, jobdb.SubmitRestartJob{
		PriorJobKey: jobdb.JobKey{TenantId: "tenant", JobId: "prior"},
		JobID:       "job-2",
	})
	if err != nil {
		t.Fatalf("SubmitRestartJob: %v", err)
	}
	if key != (jobdb.JobKey{TenantId: "tenant", JobId: "job-2"}) {
		t.Fatalf("key = %#v", key)
	}
	if submitter.calls != 2 {
		t.Fatalf("submit calls = %d, want 2", submitter.calls)
	}
	if registry.calls != 1 {
		t.Fatalf("register calls = %d, want 1", registry.calls)
	}

	wantHash, err := Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if submitter.last.Schema == nil {
		t.Fatalf("schema selector was not set")
	}
	if submitter.last.Schema.Hash != wantHash {
		t.Fatalf("schema hash = %q, want %q", submitter.last.Schema.Hash, wantHash)
	}
	if len(submitter.last.Schema.Schema) != 0 {
		t.Fatalf("expected hash-only selector, got inline schema")
	}
	if registry.last.TenantId != "tenant" {
		t.Fatalf("registered tenant = %q, want tenant", registry.last.TenantId)
	}
}

type missingSchemaSubmitter struct {
	calls int
	last  jobdb.SubmitJob
}

func (s *missingSchemaSubmitter) SubmitJob(_ context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	s.calls++
	s.last = job
	if s.calls == 1 {
		return jobdb.JobKey{}, jobdb.ErrJobSchemaNotFound
	}
	return jobdb.JobKey{TenantId: job.TenantId, JobId: job.JobID}, nil
}

type missingSchemaRestartSubmitter struct {
	calls int
	last  jobdb.SubmitRestartJob
}

func (s *missingSchemaRestartSubmitter) SubmitRestartJob(_ context.Context, job jobdb.SubmitRestartJob) (jobdb.JobKey, error) {
	s.calls++
	s.last = job
	if s.calls == 1 {
		return jobdb.JobKey{}, jobdb.ErrJobSchemaNotFound
	}
	return jobdb.JobKey{TenantId: job.PriorJobKey.TenantId, JobId: job.JobID}, nil
}

type captureRegistry struct {
	calls int
	last  jobdb.RegisterJobSchemaRequest
}

func (r *captureRegistry) RegisterJobSchema(_ context.Context, req jobdb.RegisterJobSchemaRequest) (jobdb.JobSchemaInfo, error) {
	r.calls++
	r.last = req
	hash, canonical, err := jobdb.JobSchemaHash(req.Schema)
	if err != nil {
		return jobdb.JobSchemaInfo{}, err
	}
	return jobdb.JobSchemaInfo{
		TenantId:   req.TenantId,
		SchemaHash: hash,
		Schema:     canonical,
		State:      jobdb.JobSchemaStateActive,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

func (r *captureRegistry) GetJobSchema(context.Context, jobdb.JobSchemaKey) (jobdb.JobSchemaInfo, error) {
	return jobdb.JobSchemaInfo{}, jobdb.ErrJobSchemaNotFound
}

func (r *captureRegistry) ListJobSchemas(context.Context, jobdb.ListJobSchemasRequest) (jobdb.ListJobSchemasResponse, error) {
	return jobdb.ListJobSchemasResponse{}, nil
}

func (r *captureRegistry) ArchiveJobSchema(context.Context, jobdb.JobSchemaKey) (jobdb.JobSchemaInfo, error) {
	return jobdb.JobSchemaInfo{}, jobdb.ErrJobSchemaNotFound
}

func TestSubmitterDoesNotRegisterOtherErrors(t *testing.T) {
	wantErr := errors.New("boom")
	submitter := errorSubmitter{err: wantErr}
	registry := &captureRegistry{}

	_, err := (Submitter{
		JobSubmitter: submitter,
		Registry:     registry,
	}).SubmitJob(context.Background(), jobdb.SubmitJob{TenantId: "tenant"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("SubmitJob error = %v, want %v", err, wantErr)
	}
	if registry.calls != 0 {
		t.Fatalf("register calls = %d, want 0", registry.calls)
	}
}

type errorSubmitter struct {
	err error
}

func (s errorSubmitter) SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error) {
	return jobdb.JobKey{}, s.err
}

func TestSchemaIsValidJSON(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(Schema(), &decoded); err != nil {
		t.Fatalf("schema is invalid JSON: %v", err)
	}
}
