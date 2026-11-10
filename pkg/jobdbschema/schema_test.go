package jobdbschema

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
)

func TestSchemaRegistersWithJobDB(t *testing.T) {
	ctx := celltest.Context(context.Background())
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
	ctx := celltest.Context(context.Background())
	submitter := &missingSchemaSubmitter{}
	registry := &captureRegistry{}

	keyHandle, err := (Submitter{
		JobSubmitter: submitter,
		Registry:     registry,
	}).SubmitJob(ctx, jobdb.SubmitJob{
		TenantId: "tenant",
		JobType:  "recipe",
		JobID:    "job-1", Cell: "test", CellRepo: "example.test/test",
	})
	key := keyHandle.JobKey
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
	ctx := celltest.Context(context.Background())
	submitter := &missingSchemaRestartSubmitter{}
	registry := &captureRegistry{}

	key, err := (Submitter{
		RestartSubmitter: submitter,
		Registry:         registry,
	}).SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{RequestID: "restart-id", Job: jobdb.SubmitRestartJob{
		PriorJobKey: jobdb.JobKey{TenantId: "tenant", JobId: "prior"},
		JobID:       "job-2", Cell: "test",
	}})
	if err != nil {
		t.Fatalf("SubmitRestartJob: %v", err)
	}
	if key.GoalID != "goal-2" || submitter.lastRequestID != "restart-id" {
		t.Fatal("restart lost complete identity or request ID")
	}
	if key.JobKey != (jobdb.JobKey{TenantId: "tenant", JobId: "job-2"}) {
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

func (s *missingSchemaSubmitter) SubmitJob(_ context.Context, job jobdb.SubmitJob) (jobdb.JobHandle, error) {
	s.calls++
	s.last = job
	if s.calls == 1 {
		return jobdb.JobHandle{}, jobdb.ErrJobSchemaNotFound
	}
	return jobdb.JobHandle{JobKey: jobdb.JobKey{TenantId: job.TenantId, JobId: job.JobID}, GoalID: "goal-1"}, nil
}

type missingSchemaRestartSubmitter struct {
	lastRequestID string
	calls         int
	last          jobdb.SubmitRestartJob
}

func (s *missingSchemaRestartSubmitter) SubmitRestartJob(_ context.Context, req jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	job := req.Job
	s.calls++
	s.lastRequestID = req.RequestID
	s.last = job
	if s.calls == 1 {
		return jobdb.JobHandle{}, jobdb.ErrJobSchemaNotFound
	}
	return jobdb.JobHandle{JobKey: jobdb.JobKey{TenantId: job.PriorJobKey.TenantId, JobId: job.JobID}, GoalID: "goal-2"}, nil
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
	}).SubmitJob(celltest.Context(context.Background()), jobdb.SubmitJob{TenantId: "tenant", Cell: "test", CellRepo: "example.test/test"})
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

func (s errorSubmitter) SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobHandle, error) {
	return jobdb.JobHandle{}, s.err
}

func TestSchemaIsValidJSON(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(Schema(), &decoded); err != nil {
		t.Fatalf("schema is invalid JSON: %v", err)
	}
}

func TestSubmitterPreservesPersistedSchemaSelector(t *testing.T) {
	// A replay must not replace an earlier descriptor's schema with today's hash.
	prior := &jobdb.JobSchemaSelector{Hash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	capture := &missingSchemaSubmitter{calls: 1}
	_, err := (Submitter{JobSubmitter: capture}).SubmitJob(context.Background(), jobdb.SubmitJob{TenantId: "tenant", JobID: "old", JobType: "recipe", Schema: prior})
	if err != nil {
		t.Fatal(err)
	}
	if capture.last.Schema.Hash != prior.Hash {
		t.Fatal("replaced retained schema selector")
	}
}

func TestRestoredGoalSchema(t *testing.T) {
	hash, err := Hash()
	if err != nil {
		t.Fatal(err)
	}
	if hash != "sha256:30076cc9dd60eaa75d9a2f3963d696d694eb61ea7ddb2354f9c96cc35033a4c9" {
		t.Fatalf("unexpected schema hash: %s", hash)
	}
	for _, extra := range []string{"", `,"goal_id":"stale"`, `,"unknown":true`, `,"job_id":12`} {
		t.Run(extra, func(t *testing.T) {
			rt := toyruntime.New()
			ctx := celltest.Context(context.Background())
			celltest.Create(t, rt, "tenant", "test", "example.test/test")
			_, err := rt.RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{TenantId: "tenant", Schema: Schema()})
			if err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`{"tenantId":"tenant","cell":"test","cell_repo":"example.test/test","recipe":"build","context":{},"inputs":{"goal_id":"nested"}` + extra + `}`)
			_, err = rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "dest", JobType: "recipe", Cell: "test", CellRepo: "example.test/test", Schema: &jobdb.JobSchemaSelector{Hash: hash}, Data: &jobdb.SimpleTaskData{Data: raw}}})
			if extra == "" && err != nil {
				t.Fatal(err)
			}
			if extra != "" && err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestGoalSelectionPreservesSchemaRouting(t *testing.T) {
	for _, goal := range []string{"", "selected"} {
		job := jobdb.SubmitJob{GoalID: goal}
		if err := SetSubmitJobSchema(&job); err != nil {
			t.Fatal(err)
		}
		hash, err := Hash()
		if err != nil {
			t.Fatal(err)
		}
		if job.Schema.Hash != hash {
			t.Fatal("wrong selector")
		}
		if (len(job.Schema.Schema) != 0) != (goal != "") {
			t.Fatal("changed inline schema routing")
		}
	}
}
