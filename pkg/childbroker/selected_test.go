package childbroker

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/ops/process"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestSelectedBrokerRealDispatchAndReplay(t *testing.T) {
	rt := toy.New()
	ctx := runtimetest.PrimaryGoalContext(context.Background())
	celltest.Create(t, rt, "tenant", "work", "example.test/work")
	sdk, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt}
	source, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "source", JobType: "work", Cell: "work", CellRepo: "example.test/work", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	var revoked atomic.Bool
	ctx = jobdb.WithPrimaryGoalSelectionAuthorizer(ctx, func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error {
		if revoked.Load() {
			return jobdb.ErrPrimaryGoalSelectionForbidden
		}
		return nil
	})
	current := jobcontext.Current{TenantID: "tenant", JobID: "source", GoalID: source.GoalID, InvocationHash: "invocation"}
	server, err := Start(ctx, Options{Current: current, Submitter: engine})
	require.NoError(t, err)
	defer server.Close()
	broker := jobcontext.ChildJobBroker{Endpoint: server.localEndpoint, Token: server.token, SessionID: server.sessionID}
	req, err := NewSubmitRequest(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "dest", GoalID: source.GoalID, RecipeName: "build", Cell: "work", CellRepo: "example.test/work"}, nil)
	require.NoError(t, err)
	d, err := PrepareSelected(ctx, broker, req)
	require.NoError(t, err)
	_, err = rt.GetJob(ctx, jobdb.JobKey{TenantId: "tenant", JobId: "dest"})
	require.ErrorIs(t, err, jobdb.ErrJobNotFound)
	ref, err := SubmitSelected(ctx, broker, d)
	require.NoError(t, err)
	require.Equal(t, source.GoalID, ref.GoalID)
	require.Equal(t, source.CellID, ref.CellID)
	started := server.StartedJobs()
	require.Equal(t, ref.CellID, started.Items[0].CellID)
	require.Equal(t, ref.CellRepo, started.Items[0].CellRepo)
	require.Equal(t, ref.Cell, started.Items[0].Cell)
	_, err = rt.FailGoal(ctx, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "tenant", GoalID: source.GoalID}, Message: "stop"})
	require.NoError(t, err)
	replay, err := SubmitSelected(ctx, broker, d)
	require.NoError(t, err)
	require.Equal(t, ref, replay)
	revoked.Store(true)
	_, err = SubmitSelected(ctx, broker, d)
	require.ErrorIs(t, err, jobdb.ErrPrimaryGoalSelectionForbidden)
	revoked.Store(false)
	changed := d
	changed.Job.GoalID = "missing"
	var input map[string]any
	require.NoError(t, json.Unmarshal(changed.Input, &input))
	input["goal_id"] = "missing"
	changed.Input, _ = json.Marshal(input)
	_, err = SubmitSelected(ctx, broker, changed)
	require.ErrorIs(t, err, jobdb.ErrExistingJobMismatch)
	req.Start.JobID = "after-close"
	_, err = Submit(ctx, broker, req)
	require.ErrorIs(t, err, jobdb.ErrGoalConflict)
	req.Start.GoalID = "missing"
	req.Start.JobID = "missing"
	_, err = Submit(ctx, broker, req)
	require.ErrorIs(t, err, jobdb.ErrGoalNotFound)
}
func TestSelectedBrokerOlderRoutesRedirectAndIncomplete(t *testing.T) {
	for _, mode := range []string{"old", "redirect", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			var ordinary atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == submitPath {
					ordinary.Add(1)
					w.Write([]byte(`{}`))
					return
				}
				if mode == "old" {
					http.NotFound(w, r)
					return
				}
				if mode == "redirect" {
					w.Header().Set("Location", submitPath)
					w.WriteHeader(307)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"tenant_id":"t","job_id":"dest","goal_id":"goal"}`))
			}))
			defer server.Close()
			_, err := Submit(context.Background(), jobcontext.ChildJobBroker{Endpoint: server.URL + submitPath}, SubmitRequest{Start: workflowctl.StartJob{TenantId: "t", JobID: "dest", GoalID: "goal"}})
			if mode == "incomplete" {
				require.ErrorIs(t, err, jobdb.ErrSubmissionReceiptUnavailable)
			} else {
				require.ErrorIs(t, err, jobdb.ErrPrimaryGoalSelectionUnsupported)
			}

			// Retained envelopes must choose the dedicated route without relying on
			// an optional duplicated selector in Start.
			descriptor, e := starter.NewDescriptor(context.Background(), jobdb.SubmitJob{TenantId: "t", JobID: "dest", GoalID: "goal", Data: jobdb.NewTaskDataOrPanic(nil)})
			require.NoError(t, e)
			_, e = Submit(context.Background(), jobcontext.ChildJobBroker{Endpoint: server.URL + submitPath}, SubmitRequest{Descriptor: &descriptor})
			if mode == "incomplete" {
				require.ErrorIs(t, e, jobdb.ErrSubmissionReceiptUnavailable)
			} else {
				require.ErrorIs(t, e, jobdb.ErrPrimaryGoalSelectionUnsupported)
			}
			require.Zero(t, ordinary.Load())
		})
	}
}
func TestSelectedBrokerErrorCodec(t *testing.T) {
	for _, known := range selectionErrors {
		w := httptest.NewRecorder()
		writeSelectionError(w, known)
		require.ErrorIs(t, decodeSelectionError(w.Code, w.Body.String(), true), known)
	}
}

var _ jobref.Submitter = jobdbschema.WorkflowEngine{}

type restrictedSelectedSubmitter struct{ access *permissions.Access }

func (s restrictedSelectedSubmitter) SubmitJobRef(ctx context.Context, j jobdb.SubmitJob) (jobref.Ref, error) {
	return s.access.Submit(ctx, j)
}
func TestSelectedBrokerRejectsSubmitOnlyScope(t *testing.T) {
	ctx := runtimetest.PrimaryGoalContext(context.Background())
	calls := 0
	access := permissions.NewAccess(ctx, permissions.JobDB{Jobs: permissions.JobPermissions{Submit: true}}, nil, func(context.Context, jobdb.SubmitJob) (jobref.Ref, error) { calls++; return jobref.Ref{}, nil })
	server, err := Start(ctx, Options{Current: jobcontext.Current{TenantID: "t", JobID: "parent", GoalID: "parent-goal"}, Submitter: restrictedSelectedSubmitter{access}})
	require.NoError(t, err)
	defer server.Close()
	_, err = Submit(ctx, jobcontext.ChildJobBroker{Endpoint: server.localEndpoint, Token: server.token, SessionID: server.sessionID}, SubmitRequest{Start: workflowctl.StartJob{TenantId: "t", JobID: "dest", GoalID: "goal", Cell: "work", CellRepo: "example.test/work", RecipeName: "build"}})
	require.ErrorIs(t, err, jobdb.ErrPrimaryGoalSelectionForbidden)
	require.Zero(t, calls)
}

func TestSelectedBrokerCLIProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(runtimetest.PrimaryGoalContext(context.Background()), time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "c2j")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/c2j")
	build.Dir = testRepoRoot(t)
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	rt := toy.New()
	celltest.Create(t, rt, "0", "app", "example.test/app")
	source, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "0", JobID: "source", Cell: "app", CellRepo: "example.test/app", JobType: "work", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	sdk, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt}
	ctx = jobref.WithScope(ctx, jobref.Scope{ResolveCell: func(context.Context, string) (jobdb.ResolvedCell, error) {
		return jobdb.ResolvedCell{Cell: "app", CellID: source.CellID, CellRepo: source.CellRepo}, nil
	}})
	current := jobcontext.Current{TenantID: "0", JobID: "source", GoalID: source.GoalID, InvocationHash: "process-invocation"}
	broker, err := Start(ctx, Options{Current: current, Submitter: engine})
	require.NoError(t, err)
	defer broker.Close()
	env := jobcontext.EnvForCurrent(current)
	for k, v := range broker.Env() {
		env[k] = v
	}
	workspace := t.TempDir()
	writeWorkerBrokerWorkspace(t, workspace)
	requestFile := filepath.Join(workspace, "request.json")
	stdout, stderr, err := process.ExecuteProcess(ctx, process.RunRequest{WorkspaceRoot: workspace, WorkingDir: workspace, Command: []string{binary, "submit", "New instructions", "--embed", "--cell", "app", "--cell-repo", "example.test/app", "--advanced-recipe-file", "child.yaml", "--goal", source.GoalID, "--job-id", "cli-selected", "--request-file", requestFile, "--json"}, Env: env})
	require.NoError(t, err, string(stderr))
	var accepted SubmitResponse
	require.NoError(t, json.Unmarshal(stdout, &accepted))
	require.Equal(t, source.CellID, accepted.CellID)
	require.Equal(t, source.GoalID, accepted.GoalID)
	require.Equal(t, "cli-selected", accepted.JobID)
	require.NoError(t, os.Remove(filepath.Join(workspace, "child.yaml")))
	stdout, stderr, err = process.ExecuteProcess(ctx, process.RunRequest{WorkspaceRoot: workspace, WorkingDir: workspace, Command: []string{binary, "submit", "--embed", "--replay", requestFile, "--json"}, Env: env})
	require.NoError(t, err, string(stderr))
	var replay SubmitResponse
	require.NoError(t, json.Unmarshal(stdout, &replay))
	require.Equal(t, accepted, replay)
}

func TestSelectedBrokerLegacyOrdinaryStillUsable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != submitPath {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tenant_id":"t","job_id":"ordinary","goal_id":"new-goal"}`))
	}))
	defer server.Close()
	ref, err := Submit(context.Background(), jobcontext.ChildJobBroker{Endpoint: server.URL + submitPath}, SubmitRequest{Start: workflowctl.StartJob{TenantId: "t", JobID: "ordinary", Cell: "work", RecipeName: "build"}})
	require.NoError(t, err)
	require.Equal(t, "new-goal", ref.GoalID)
	require.Empty(t, ref.CellID, "historical ordinary receipt validation remains compatible")
}
