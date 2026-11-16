package story_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/jobrestart"
	restartservice "github.com/colony-2/c2j/pkg/jobrestart/service"
	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/story"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type unavailableRecipeRuntime struct {
	jobdb.WorkflowRuntime
	cause error
}

func (r unavailableRecipeRuntime) OpenArtifact(context.Context, jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	return nil, r.cause
}
func TestRestartDiscoveryWithMissingRecipeBytes(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, restartSequenceRecipe)
			before := restartStoredState(t, f)
			f.guard.watch()
			cause := errors.New("retained recipe bytes expired")
			engine, err := wf.NewEngineBuilder().WithRuntime(unavailableRecipeRuntime{f.runtime, cause}).WithWorkerTenantId(f.key.TenantId).PlusWorkers(f.worker).BuildEngine()
			require.NoError(t, err)
			svc := &restartservice.Service{Engine: engine, Tenant: f.key.TenantId}
			points, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "points", JobID: f.key.JobId})
			require.NoError(t, err)
			require.NotEmpty(t, points.Points.RestartScopes)
			var scope string
			for _, point := range points.Points.RestartScopes {
				if strings.Contains(point.ScopeID, `scope:"op:target"`) {
					scope = point.ScopeID
				}
			}
			require.NotEmpty(t, scope)
			result, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Scope: scope}})
			require.ErrorIs(t, err, story.ErrRestartContextUnavailable)
			require.ErrorContains(t, err, cause.Error())
			require.Nil(t, result.Context)
			require.Equal(t, int32(4), f.task.calls.Load())
			zero := int64(0)
			submission, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Chapter: &zero}})
			require.NoError(t, err, "chapter zero does not need recipe bytes")
			require.Zero(t, submission.Context.ChapterOrdinal)
			f.guard.assertReadOnly(t)
			require.Equal(t, before, restartStoredState(t, f))
		})
	}
}
func TestRestartInspectionBeforeInvalidInputsPreservesReferences(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			digest := strings.Repeat("a", 64)
			key := jobdb.ArtifactKey{JobId: "not-readable", TaskOrdinal: 1, Name: objects.ArtifactPrefix + digest + ".tar", SizeBytes: 12}
			input := map[string]any{"configuration": configuration.Reference{Kind: "config", Name: "not-resolved", ExpectedType: "string"}, "object": objects.Ref{Format: objects.Version, Type: "fixture/v1", TenantID: "restart-tests", Artifact: key, SHA256: digest}, "artifact": artifacts.NewStoredRef(key)}
			f := newRestartFixtureInputs(t, backend, `schema_version: 2
id: safety
input_schema:
 configuration: {type: string}
 object: {type: checkpoint, object_type: fixture/v1}
 artifact: {type: artifact}
inputs:
 configuration: '${{ inputs.configuration }}'
 object: '${{ inputs.object }}'
 artifact: '${{ inputs.artifact }}'
sequence:
 - id: target
   op: restart_context_probe
   inputs: {value: '${{ inputs.missing }}'}
`, input, true)
			storedBefore := restartStoredState(t, f)
			f.guard.watch()
			before, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			svc := &restartservice.Service{Engine: f.engine, Tenant: f.key.TenantId}
			points, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "points", JobID: f.key.JobId})
			require.NoError(t, err)
			var scope string
			for _, point := range points.Points.RestartScopes {
				if strings.Contains(point.ScopeID, `scope:"op:target"`) {
					scope = point.ScopeID
				}
			}
			require.NotEmpty(t, scope, "%+v", *before.Attempts[0].Outcome.Error)
			result, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Scope: scope}})
			require.NoError(t, err)
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			visible, err := json.Marshal(result.Context.Visible["inputs"])
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(visible))
			zero := int64(0)
			submission, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Chapter: &zero}})
			require.NoError(t, err)
			require.NotContains(t, submission.Context.Visible, "sequence")
			submitted, err := json.Marshal(submission.Context.Visible["inputs"])
			require.NoError(t, err)
			require.JSONEq(t, string(raw), string(submitted))

			require.Zero(t, f.task.calls.Load(), "selected inputs fail when evaluated; inspection stops before they resolve")
			after, err := f.engine.GetJobRun(f.ctx, jobdb.GetJobRunRequest{JobKey: f.key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			require.Equal(t, before, after)
			f.guard.assertReadOnly(t)
			require.Equal(t, storedBefore, restartStoredState(t, f))
		})
	}
}

// The concrete embedding preserves optional runtime capabilities (including
// schema registration and HTTP lease operations). During observation every
// write is counted before refusal, including requests carrying artifact uploads.
type inspectionRuntime struct {
	*sqlite.Runtime
	mu       sync.Mutex
	watching bool
	attempts []string
}

func (r *inspectionRuntime) watch() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.watching = true
	r.attempts = nil
}
func (r *inspectionRuntime) mutation(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.watching {
		return nil
	}
	r.attempts = append(r.attempts, name)
	return fmt.Errorf("inspection attempted mutation: %s", name)
}
func (r *inspectionRuntime) assertReadOnly(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.Empty(t, r.attempts, "includes refused chapter/artifact/upload/submission/lifecycle attempts")
}

func (r *inspectionRuntime) SubmitJob(ctx context.Context, q jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	if err := r.mutation("SubmitJob"); err != nil {
		var zero jobdb.JobHandle
		return zero, err
	}
	return r.Runtime.SubmitJob(ctx, q)
}

func (r *inspectionRuntime) SubmitRestartJob(ctx context.Context, q jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	if err := r.mutation("SubmitRestartJob"); err != nil {
		var zero jobdb.JobHandle
		return zero, err
	}
	return r.Runtime.SubmitRestartJob(ctx, q)
}

func (r *inspectionRuntime) CancelJob(ctx context.Context, q jobdb.CancelJobRequest) error {
	if err := r.mutation("CancelJob"); err != nil {
		return err
	}
	return r.Runtime.CancelJob(ctx, q)
}

func (r *inspectionRuntime) ClearCrashConcern(ctx context.Context, q jobdb.ClearCrashConcernRequest) error {
	if err := r.mutation("ClearCrashConcern"); err != nil {
		return err
	}
	return r.Runtime.ClearCrashConcern(ctx, q)
}

func (r *inspectionRuntime) PollWork(ctx context.Context, q jobdb.PollWorkRequest) ([]jobdb.ExecutionLease, error) {
	if err := r.mutation("PollWork"); err != nil {
		var zero []jobdb.ExecutionLease
		return zero, err
	}
	return r.Runtime.PollWork(ctx, q)
}

func (r *inspectionRuntime) GetJobLease(ctx context.Context, q jobdb.GetJobLeaseRequest) (jobdb.ExecutionLease, error) {
	if err := r.mutation("GetJobLease"); err != nil {
		var zero jobdb.ExecutionLease
		return zero, err
	}
	return r.Runtime.GetJobLease(ctx, q)
}

func (r *inspectionRuntime) CompleteTaskIfWaiting(ctx context.Context, q jobdb.CompleteTaskIfWaitingRequest) error {
	if err := r.mutation("CompleteTaskIfWaiting"); err != nil {
		return err
	}
	return r.Runtime.CompleteTaskIfWaiting(ctx, q)
}

func (r *inspectionRuntime) PutChapter(ctx context.Context, q jobdb.PutChapterRequest) error {
	if err := r.mutation("PutChapter"); err != nil {
		return err
	}
	return r.Runtime.PutChapter(ctx, q)
}

func (r *inspectionRuntime) CreateCell(ctx context.Context, q jobdb.CreateCellRequest) (jobdb.Cell, error) {
	if err := r.mutation("CreateCell"); err != nil {
		var zero jobdb.Cell
		return zero, err
	}
	return r.Runtime.CreateCell(ctx, q)
}

func (r *inspectionRuntime) UpdateCell(ctx context.Context, q jobdb.UpdateCellRequest) (jobdb.Cell, error) {
	if err := r.mutation("UpdateCell"); err != nil {
		var zero jobdb.Cell
		return zero, err
	}
	return r.Runtime.UpdateCell(ctx, q)
}

func (r *inspectionRuntime) RetireCell(ctx context.Context, q jobdb.RetireCellRequest) (jobdb.Cell, error) {
	if err := r.mutation("RetireCell"); err != nil {
		var zero jobdb.Cell
		return zero, err
	}
	return r.Runtime.RetireCell(ctx, q)
}

func (r *inspectionRuntime) AddGoalCandidate(ctx context.Context, q jobdb.AddGoalCandidateRequest) (jobdb.GoalCandidateInfo, error) {
	if err := r.mutation("AddGoalCandidate"); err != nil {
		var zero jobdb.GoalCandidateInfo
		return zero, err
	}
	return r.Runtime.AddGoalCandidate(ctx, q)
}

func (r *inspectionRuntime) FailGoal(ctx context.Context, q jobdb.FailGoalRequest) (jobdb.GoalInfo, error) {
	if err := r.mutation("FailGoal"); err != nil {
		var zero jobdb.GoalInfo
		return zero, err
	}
	return r.Runtime.FailGoal(ctx, q)
}

func (r *inspectionRuntime) RegisterJobSchema(ctx context.Context, q jobdb.RegisterJobSchemaRequest) (jobdb.JobSchemaInfo, error) {
	if err := r.mutation("RegisterJobSchema"); err != nil {
		var zero jobdb.JobSchemaInfo
		return zero, err
	}
	return r.Runtime.RegisterJobSchema(ctx, q)
}

func (r *inspectionRuntime) ArchiveJobSchema(ctx context.Context, q jobdb.JobSchemaKey) (jobdb.JobSchemaInfo, error) {
	if err := r.mutation("ArchiveJobSchema"); err != nil {
		var zero jobdb.JobSchemaInfo
		return zero, err
	}
	return r.Runtime.ArchiveJobSchema(ctx, q)
}

func (r *inspectionRuntime) KeepAliveLeaseByID(ctx context.Context, k jobdb.JobKey, id, worker string, duration time.Duration) error {
	if err := r.mutation("KeepAliveLeaseByID"); err != nil {
		return err
	}
	return r.Runtime.KeepAliveLeaseByID(ctx, k, id, worker, duration)
}

func (r *inspectionRuntime) KeepAliveLeaseByIDWithExpiry(ctx context.Context, k jobdb.JobKey, id, worker string, duration time.Duration) (time.Time, error) {
	if err := r.mutation("KeepAliveLeaseByIDWithExpiry"); err != nil {
		var zero time.Time
		return zero, err
	}
	return r.Runtime.KeepAliveLeaseByIDWithExpiry(ctx, k, id, worker, duration)
}

func (r *inspectionRuntime) CompleteJobWithLeaseByID(ctx context.Context, k jobdb.JobKey, id, worker string, q jobdb.CompleteExecutionRequest) error {
	if err := r.mutation("CompleteJobWithLeaseByID"); err != nil {
		return err
	}
	return r.Runtime.CompleteJobWithLeaseByID(ctx, k, id, worker, q)
}

func (r *inspectionRuntime) RescheduleJobWithLeaseByID(ctx context.Context, k jobdb.JobKey, id, worker string, q jobdb.RescheduleExecutionRequest) error {
	if err := r.mutation("RescheduleJobWithLeaseByID"); err != nil {
		return err
	}
	return r.Runtime.RescheduleJobWithLeaseByID(ctx, k, id, worker, q)
}

func (r *inspectionRuntime) SubmitJobWithLeaseByID(ctx context.Context, k jobdb.JobKey, id, worker string, q jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	if err := r.mutation("SubmitJobWithLeaseByID"); err != nil {
		var zero jobdb.JobHandle
		return zero, err
	}
	return r.Runtime.SubmitJobWithLeaseByID(ctx, k, id, worker, q)
}

func (r *inspectionRuntime) SubmitRestartJobWithLeaseByID(ctx context.Context, k jobdb.JobKey, id, worker string, q jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	if err := r.mutation("SubmitRestartJobWithLeaseByID"); err != nil {
		var zero jobdb.JobHandle
		return zero, err
	}
	return r.Runtime.SubmitRestartJobWithLeaseByID(ctx, k, id, worker, q)
}

// Snapshot every SQLite user table, including chapter bodies, inline artifact
// bytes, upload metadata, requests, jobs, goals and leases. Sort row encodings
// instead of relying on SQLite row order. Hash only to keep failures bounded.
func restartStoredState(t *testing.T, f *restartFixture) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer db.Close()
	tables, err := db.QueryContext(f.ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	require.NoError(t, err)
	var names []string
	for tables.Next() {
		var name string
		require.NoError(t, tables.Scan(&name))
		names = append(names, name)
	}
	require.NoError(t, tables.Err())
	require.NoError(t, tables.Close())
	out := map[string]string{}
	for _, name := range names {
		rows, err := db.QueryContext(f.ctx, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`)
		require.NoError(t, err)
		columns, err := rows.Columns()
		require.NoError(t, err)
		var encoded []string
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			require.NoError(t, rows.Scan(pointers...))
			raw, err := json.Marshal(values)
			require.NoError(t, err)
			encoded = append(encoded, string(raw))
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		sort.Strings(encoded)
		raw, err := json.Marshal(encoded)
		require.NoError(t, err)
		out[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	// Read referenced artifact bytes as well, covering both inline and blob storage.
	chapters, err := f.runtime.ListChapters(f.ctx, jobdb.ListChaptersRequest{JobKey: f.key})
	require.NoError(t, err)
	count := 0
	for _, ch := range chapters {
		for _, a := range ch.Artifacts {
			reader, err := f.runtime.OpenArtifact(f.ctx, jobdb.ArtifactRef{JobKey: f.key, Ordinal: ch.Ordinal, Name: a.Name, Digest: a.Digest})
			require.NoError(t, err)
			stream, err := reader.Open()
			require.NoError(t, err)
			raw, err := io.ReadAll(stream)
			require.NoError(t, err)
			require.NoError(t, stream.Close())
			out[fmt.Sprintf("artifact/%d/%s", ch.Ordinal, a.Name)] = fmt.Sprintf("%d/%x", len(raw), sha256.Sum256(raw))
			count++
		}
	}
	require.Positive(t, count, "retained recipe artifact makes artifact preservation meaningful")
	// Include unreferenced blob/upload files too; inventory reads alone would
	// miss an inspection that staged bytes but never attached an artifact.
	blobDir := f.dbPath + ".blobs"
	err = filepath.WalkDir(blobDir, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == blobDir {
			return nil
		} // frozen fixtures keep all artifacts inline
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(blobDir, path)
		if err != nil {
			return err
		}
		out["blob/"+filepath.ToSlash(name)] = fmt.Sprintf("%d/%x", len(raw), sha256.Sum256(raw))
		return nil
	})
	require.NoError(t, err)
	return out
}

// Truncate only this disposable replay view before a required accepted task
// result. Discovery uses complete recorded metadata; reconstruction must not
// fill the missing result by calling a worker or writing a replacement.
type missingResultRuntime struct {
	jobdb.WorkflowRuntime
	ordinal int64
}

func (r missingResultRuntime) ListChapters(ctx context.Context, q jobdb.ListChaptersRequest) ([]jobdb.Chapter, error) {
	chapters, err := r.WorkflowRuntime.ListChapters(ctx, q)
	if err != nil {
		return nil, err
	}
	var retained []jobdb.Chapter
	for _, chapter := range chapters {
		if chapter.Ordinal < r.ordinal {
			retained = append(retained, chapter)
		}
	}
	return retained, nil
}
func (r missingResultRuntime) GetChapter(ctx context.Context, q jobdb.ChapterRef) (jobdb.Chapter, error) {
	if q.Ordinal >= r.ordinal {
		return jobdb.Chapter{}, fmt.Errorf("retained task result missing at chapter %d", q.Ordinal)
	}
	return r.WorkflowRuntime.GetChapter(ctx, q)
}
func TestRestartInspectionMissingRequiredResult(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newRestartFixture(t, backend, restartSequenceRecipe)
			ordinal := firstTaskResultOrdinal(t, f)
			engine, err := wf.NewEngineBuilder().WithRuntime(missingResultRuntime{f.runtime, ordinal}).WithWorkerTenantId(f.key.TenantId).PlusWorkers(f.worker, f.task).BuildEngine()
			require.NoError(t, err)
			svc := &restartservice.Service{Engine: restartInspectionReadView{Engine: engine, source: f.engine}, Tenant: f.key.TenantId}
			before := restartStoredState(t, f)
			f.guard.watch()
			points, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "points", JobID: f.key.JobId})
			require.NoError(t, err)
			point := discoveredRestartScope(t, points.Points, "op:target")
			result, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "context", JobID: f.key.JobId, Selector: jobrestart.Selector{Scope: point.ScopeID}})
			require.ErrorIs(t, err, story.ErrRestartContextUnavailable)
			require.ErrorContains(t, err, "missing")
			require.Nil(t, result.Context)
			require.EqualValues(t, 4, f.task.calls.Load(), "missing-result callback must not run")
			f.guard.assertReadOnly(t)
			require.Equal(t, before, restartStoredState(t, f))
			t.Logf("missing required result: %v; callback delta=0; mutation attempts=0; stored state unchanged", err)
		})
	}
}

// Discovery retains the complete metadata view, while the isolated replay view
// lacks the required result and everything after it. No persisted rows change.
type restartInspectionReadView struct {
	wf.Engine
	source wf.Engine
}

func (e restartInspectionReadView) GetJobRun(ctx context.Context, q jobdb.GetJobRunRequest) (jobdb.GetJobRunResponse, error) {
	return e.source.GetJobRun(ctx, q)
}

func TestRestartInspectionSuccessfulAndOrdinaryFailure(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%v", backend, fail), func(t *testing.T) {
				f := newRestartFixtureFailure(t, backend, `id: regression
sequence:
 - id: prior
   op: restart_context_probe
   inputs: {value: accepted-prior}
 - id: target
   op: restart_context_probe
   inputs: {value: ordinary-failure}
outputs: {value: '${{ sequence.target.outputs.value }}'}
`, nil, "ordinary-failure", fail)
				suffix := "op:target"
				before := restartStoredState(t, f)
				calls := f.task.calls.Load()
				f.guard.watch()
				svc := &restartservice.Service{Engine: f.engine, Tenant: f.key.TenantId}
				points, err := svc.Restart(f.ctx, jobrestart.Request{Operation: "points", JobID: f.key.JobId})
				require.NoError(t, err)
				point := discoveredRestartScope(t, points.Points, suffix)
				snapshot := f.inspectScope(t, f.key, point.ScopeID)
				assertAcceptedPrior(t, snapshot.Visible, true)
				require.Contains(t, snapshot.RedactedPaths, "/visible/sequence/prior/outputs/api_key")
				require.Equal(t, calls, f.task.calls.Load())
				f.guard.assertReadOnly(t)
				require.Equal(t, before, restartStoredState(t, f))
			})
		}
	}
}

func (r *inspectionRuntime) RenewExecutionLeaseByID(ctx context.Context, key jobdb.JobKey, id, worker string, duration time.Duration) (jobdb.RenewableExecutionLease, error) {
	if err := r.mutation("RenewExecutionLeaseByID"); err != nil {
		return nil, err
	}
	return r.Runtime.RenewExecutionLeaseByID(ctx, key, id, worker, duration)
}
