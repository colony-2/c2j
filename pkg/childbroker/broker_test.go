package childbroker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func TestBrokerSubmitUsesLeaseSubmitterAndOverridesParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	submitter := &captureSubmitter{}
	broker, err := Start(ctx, Options{
		Current: jobcontext.Current{
			TenantID:           "tenant",
			JobID:              "parent",
			JobType:            starter.RecipeJobType,
			OpType:             "command_execution",
			OpStep:             "run",
			InvocationSequence: 3,
			InvocationHash:     "hash-1",
		},
		Submitter: submitter,
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer broker.Close()

	req, err := NewSubmitRequest(ctx, workflowctl.StartJob{
		TenantId:   "tenant",
		JobID:      "child",
		RecipeName: "deploy",
		Parent: &jobcontext.Parent{
			TenantID:       "tenant",
			JobID:          "spoofed",
			InvocationHash: "spoofed",
		},
		JobContext: contextual.JobContext{
			Workflow: contextual.WorkflowContext{ProjectId: "tenant", CellName: "cell"},
		},
	}, []jobdb.Artifact{jobdb.NewArtifactFromBytes("input.txt", []byte("hello"))})
	if err != nil {
		t.Fatalf("NewSubmitRequest(): %v", err)
	}

	resp, err := Submit(ctx, brokerConfig(t, broker), req)
	if err != nil {
		t.Fatalf("Submit(): %v", err)
	}
	if resp.TenantID != "tenant" || resp.JobID != "child" || resp.Recipe != "deploy" {
		t.Fatalf("unexpected response: %#v", resp)
	}
	if submitter.calls != 1 {
		t.Fatalf("submit calls = %d, want 1", submitter.calls)
	}
	if submitter.last.TenantId != "tenant" || submitter.last.JobID != "child" {
		t.Fatalf("unexpected submitted job: %#v", submitter.last)
	}
	artifacts, err := submitter.last.Data.GetArtifacts()
	if err != nil {
		t.Fatalf("GetArtifacts(): %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Name() != "input.txt" {
		t.Fatalf("unexpected artifacts: %#v", artifacts)
	}

	var meta starter.JobMetadata
	if err := json.Unmarshal(submitter.last.Metadata, &meta); err != nil {
		t.Fatalf("metadata decode: %v", err)
	}
	if meta.ParentJobID != "parent" || meta.ParentInvocationHash != "hash-1" || meta.ParentOpStep != "run" {
		t.Fatalf("broker did not override parent metadata: %#v", meta)
	}

	started := broker.StartedJobs()
	if len(started.JobIDs) != 1 || started.JobIDs[0] != "child" {
		t.Fatalf("unexpected started jobs: %#v", started)
	}
}

func TestBrokerRejectsCrossTenantSubmit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	broker, err := Start(ctx, Options{
		Current:   jobcontext.Current{TenantID: "tenant", JobID: "parent"},
		Submitter: &captureSubmitter{},
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer broker.Close()

	_, err = Submit(ctx, brokerConfig(t, broker), SubmitRequest{
		Start: workflowctl.StartJob{
			TenantId:   "other",
			RecipeName: "deploy",
		},
	})
	if err == nil {
		t.Fatal("expected cross-tenant submit to fail")
	}
}

func TestBrokerSubmitReturnsAssignedJobIDWhenStartOmitsJobID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	submitter := &captureSubmitter{}
	broker, err := Start(ctx, Options{
		Current: jobcontext.Current{
			TenantID:       "tenant",
			JobID:          "parent",
			InvocationHash: "hash-1",
		},
		Submitter: submitter,
	})
	if err != nil {
		t.Fatalf("Start(): %v", err)
	}
	defer broker.Close()

	resp, err := Submit(ctx, brokerConfig(t, broker), SubmitRequest{
		Start: workflowctl.StartJob{
			TenantId:   "tenant",
			RecipeName: "deploy",
		},
	})
	if err != nil {
		t.Fatalf("Submit(): %v", err)
	}
	if resp.JobID != "captured-child" {
		t.Fatalf("response job id = %q, want assigned id", resp.JobID)
	}
	if submitter.last.JobID != "" {
		t.Fatalf("submitted request job id = %q, want empty request id", submitter.last.JobID)
	}

	started := broker.StartedJobs()
	if len(started.JobIDs) != 1 || started.JobIDs[0] != "captured-child" {
		t.Fatalf("unexpected started jobs: %#v", started)
	}
}

func brokerConfig(t *testing.T, broker *Server) jobcontext.ChildJobBroker {
	t.Helper()
	cfg, ok, err := jobcontext.ChildJobBrokerFromEnv(func(key string) string { return broker.Env()[key] })
	if err != nil {
		t.Fatalf("ChildJobBrokerFromEnv(): %v", err)
	}
	if !ok {
		t.Fatal("missing broker env")
	}
	return cfg
}

type captureSubmitter struct {
	calls   int
	last    jobdb.SubmitJob
	lastKey jobdb.JobKey
}

func (s *captureSubmitter) SubmitJob(_ context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	s.calls++
	s.last = job
	jobID := job.JobID
	if jobID == "" {
		jobID = "captured-child"
	}
	s.lastKey = jobdb.JobKey{TenantId: job.TenantId, JobId: jobID}
	return s.lastKey, nil
}
