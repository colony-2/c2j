// Separate-process HTTP receipt fixture pinned to the required legacy JobDB.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
)

type call struct {
	Method, Path string
	Body         json.RawMessage
}
type cohort struct {
	Tenant              string
	Descriptor          any
	Restart             jobdb.SubmitRestartJobRequest
	Selected, Restarted jobdb.JobHandle
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx := celltest.Context(context.Background())
	db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(os.Args[1], "legacy.db")})
	if err != nil {
		return err
	}
	defer db.Close(ctx)
	var mu sync.Mutex
	var calls []call
	var cohorts []cohort
	handler := remote.NewConsumerServer(db)
	mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_fixture/state" {
			counts := map[string]int{}
			for _, c := range []string{"fixed", "generated"} {
				list, e := db.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{c}})
				if e != nil {
					http.Error(w, e.Error(), 500)
					return
				}
				counts[c] = len(list.Jobs)
			}
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"counts": counts, "calls": calls})
			return
		}
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		if len(b) == 0 {
			b = nil
		}
		mu.Lock()
		calls = append(calls, call{r.Method, r.URL.Path, b})
		mu.Unlock()
		current := celltest.Context(r.Context())
		if r.Header.Get("X-Fixture-Deny") == "submit" {
			current = jobdb.WithCellAuthority(current, jobdb.CellAuthority{Principal: "revoked", TenantID: "*", Permissions: map[string]bool{}})
		}
		current = jobdb.WithPrimaryGoalSelectionAuthorizer(current, func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error {
			if r.Header.Get("X-Fixture-Deny") == "selection" {
				return jobdb.ErrPrimaryGoalSelectionForbidden
			}
			return nil
		})
		handler.ServeHTTP(w, r.WithContext(current))
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux}
	defer server.Close()
	go server.Serve(listener)
	address := "http://" + listener.Addr().String()
	client, err := remote.New(address, nil)
	if err != nil {
		return err
	}
	for _, tenant := range []string{"fixed", "generated"} {
		source, err := celltest.Submit(db, ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: tenant, JobID: "source", Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)}})
		if err != nil {
			return err
		}
		input := json.RawMessage(`{"inputs":{"n":9007199254740993,"literal":"{{ unchanged }}"}}`)
		artifact := []byte("original artifact\x00bytes")
		timeout := jobdb.Duration(30 * time.Second)
		job := jobdb.SubmitJob{TenantId: tenant, JobID: "selected", GoalID: source.GoalID, Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: &jobdb.SimpleTaskData{Data: input, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("proof", artifact)}}, Metadata: json.RawMessage(`{"n":9007199254740993,"nested":[null,true,1.5,{"s":"original"}]}`), RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 3, InitialInterval: jobdb.Duration(time.Second)}, TotalTimeout: &timeout}, Dependencies: []jobdb.Dependency{{Target: jobdb.DependencyTarget{JobID: "source"}, Condition: jobdb.JobPrereqComplete}}}
		if tenant == "generated" {
			job.Dependencies = nil
			job.Prerequisites = []jobdb.JobPrerequisite{{JobID: "source", Condition: jobdb.JobPrereqComplete}}
		}
		accepted, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
		if err != nil {
			return fmt.Errorf("seed selected: %w", err)
		}
		again, err := client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
		if err != nil || again != accepted {
			return fmt.Errorf("old selected replay: %v", err)
		}
		restart := jobdb.SubmitRestartJobRequest{RequestID: "original-restart", WorkerID: "original-worker", RequestTime: time.Date(2026, 11, 19, 1, 2, 3, 0, time.UTC), Job: jobdb.SubmitRestartJob{PriorJobKey: source.JobKey, Cell: "app", CellRepo: "example.test/app", ShareGoal: true, LastStepToKeep: 0, ExtraTaskInput: jobdb.NewTaskDataOrPanic(map[string]any{"n": json.Number("9007199254740993")}), ExtraTaskOutput: jobdb.NewTaskDataOrPanic(map[string]any{"original": true})}}
		if tenant == "fixed" {
			restart.Job.JobID = "restarted"
		}
		restarted, err := client.SubmitRestartJob(ctx, restart)
		if err != nil {
			return fmt.Errorf("seed restart: %w", err)
		}
		again, err = client.SubmitRestartJob(ctx, restart)
		if err != nil || again != restarted {
			return fmt.Errorf("old restart replay: %v", err)
		}
		sum := sha256.Sum256(artifact)
		saved := job
		saved.Data = nil
		descriptor := map[string]any{"version": 1, "operation": "submit", "job": saved, "input": input, "artifacts": []any{map[string]any{"name": "proof", "data": artifact, "sha256": hex.EncodeToString(sum[:])}}}
		cohorts = append(cohorts, cohort{tenant, descriptor, restart, accepted, restarted})
	}
	vectors, err := codecVectors(ctx)
	if err != nil {
		return err
	}
	// Native interface task data needs a concrete decoder at the current client.
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"url": address, "cohorts": cohorts, "vectors": vectors}); err != nil {
		return err
	}
	select {}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Codec vectors include fields that cannot all be combined in one valid stored
// operation. Their expected bodies come from the unmodified legacy SDK.
type vector struct {
	Descriptor any
	Restart    *jobdb.SubmitRestartJobRequest
	Calls      []call
	Receipt    jobdb.JobHandle
}
type recorder struct {
	calls   []call
	receipt jobdb.JobHandle
}

func (r *recorder) RoundTrip(q *http.Request) (*http.Response, error) {
	var b []byte
	var err error
	if q.Body != nil {
		b, err = io.ReadAll(q.Body)
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		b = nil
	}
	r.calls = append(r.calls, call{q.Method, q.URL.Path, b})
	body := mustJSON(r.receipt)
	if q.Method == http.MethodGet {
		body = []byte(`{"supported":true}`)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}
func codecVectors(ctx context.Context) ([]vector, error) {
	h := jobdb.JobHandle{JobKey: jobdb.JobKey{TenantId: "codec", JobId: "destination"}, GoalID: "goal", Cell: "app", CellID: "captured-cell", CellRepo: "example.test/app"}
	r := &recorder{receipt: h}
	client, err := remote.New("http://codec.invalid", &http.Client{Transport: r})
	if err != nil {
		return nil, err
	}
	timeout := jobdb.Duration(40 * time.Second)
	now := time.Date(2026, 11, 19, 2, 3, 4, 567, time.UTC)
	update := &jobdb.ClientPayloadUpdate{Mode: "reset", Value: json.RawMessage(`{"n":9007199254740993}`)}
	selector := &jobdb.JobSchemaSelector{Hash: "retained-hash", Schema: json.RawMessage(`{"version":7}`)}
	job := jobdb.SubmitJob{TenantId: "codec", JobID: "destination", GoalID: "goal", Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil), Metadata: json.RawMessage(`{"int":9007199254740993,"double":1.2e3,"emptyMap":{},"emptyList":[]}`), Schema: selector, ClientPayloadUpdate: update, AvailableAt: &now, Dependencies: []jobdb.Dependency{{Target: jobdb.DependencyTarget{GoalID: "another-goal"}, Condition: jobdb.JobPrereqSuccess}}, Prerequisites: []jobdb.JobPrerequisite{{JobID: "source", Condition: jobdb.JobPrereqComplete}}, RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{InitialInterval: jobdb.Duration(time.Second), MaximumInterval: jobdb.Duration(5 * time.Second), BackoffCoefficient: 2, MaximumAttempts: 3, NonRetryableErrorTypes: []string{"fatal"}}, InvocationTimeout: &timeout, TotalTimeout: &timeout}}
	if _, err = client.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job}); err != nil {
		return nil, err
	}
	job.Data = nil
	vectors := []vector{{Descriptor: map[string]any{"version": 1, "operation": "submit", "job": job, "input": json.RawMessage(`null`)}, Calls: r.calls, Receipt: h}}
	r.calls = nil
	restart := jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: jobdb.JobKey{TenantId: "codec", JobId: "source"}, JobID: "destination", Cell: "app", CellRepo: "example.test/app", ShareGoal: true, LastStepToKeep: 9007199254740993, Schema: selector, ClientPayloadUpdate: update, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: "frozen-scope", Payload: json.RawMessage(`{"n":9007199254740993}`)}, Dependencies: job.Dependencies, Prerequisites: job.Prerequisites}, RequestID: "original-id", WorkerID: "original-worker", RequestTime: now}
	if _, err = client.SubmitRestartJob(ctx, restart); err != nil {
		return nil, err
	}
	vectors = append(vectors, vector{Restart: &restart, Calls: r.calls, Receipt: h})
	return vectors, nil
}
