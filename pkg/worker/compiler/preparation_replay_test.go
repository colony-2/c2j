package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/toolenv"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestEmptyPreparationRecoveryRejectsOtherChanges(t *testing.T) {
	original := []byte(`{"preparation":{},"input":{"large":9007199254740993},"context":{"invoke_seq":0}}`)
	artifact := jobdb.NewArtifactFromBytes("pack", []byte("original"))
	hash, err := legacyTaskInputHash(original, []jobdb.Artifact{artifact})
	require.NoError(t, err)
	for _, tc := range []struct {
		name, current              string
		changedArtifact, wrongTask bool
		accept                     bool
	}{
		{name: "empty omitted", current: `{"input":{"large":9007199254740993},"context":{"invoke_seq":0}}`, accept: true},
		{name: "rounded input", current: `{"input":{"large":9007199254740992},"context":{"invoke_seq":0}}`},
		{name: "unknown field", current: `{"extra":true,"input":{"large":9007199254740993},"context":{"invoke_seq":0}}`},
		{name: "nonempty preparation", current: `{"preparation":{"extension":{"selector":"nix:abc#op"}},"input":{"large":9007199254740993},"context":{"invoke_seq":0}}`},
		{name: "null preparation", current: `{"preparation":null,"input":{"large":9007199254740993},"context":{"invoke_seq":0}}`},
		{name: "changed artifact", current: `{"input":{"large":9007199254740993},"context":{"invoke_seq":0}}`, changedArtifact: true},
		{name: "different recorded task", current: `{"input":{"large":9007199254740993},"context":{"invoke_seq":0}}`, wrongTask: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate HTTP's reordered cached JSON, retaining its original hash.
			var fields map[string]any
			d := json.NewDecoder(bytes.NewReader(original))
			d.UseNumber()
			require.NoError(t, d.Decode(&fields))
			cached, err := json.Marshal(fields)
			require.NoError(t, err)
			mismatch := jobworkflow.TaskInputMismatchError{TaskType: "probe:run", CachedInput: cached, CachedInputHash: hash, Meta: jobworkflow.TaskDeterminismMeta{TaskType: "probe:run"}}
			if tc.wrongTask {
				mismatch.Meta.TaskType = "other:run"
			}
			art := artifact
			if tc.changedArtifact {
				art = jobdb.NewArtifactFromBytes("pack", []byte("changed"))
			}
			data := &jobdb.SimpleTaskData{Data: []byte(tc.current), Artifacts: []jobdb.Artifact{art}}
			options := jobworkflow.TaskOptions{Alternate: &jobworkflow.TaskAlternate{TaskType: "alternate", At: time.Unix(100, 0)}}
			called := false
			_, ok, err := recoverEmptyPreparation("probe:run", jobworkflow.WithTaskOptions(data, options), mismatch, func(td jobdb.TaskData) (jobdb.TaskData, error) {
				called = true
				raw, err := td.GetData()
				require.NoError(t, err)
				require.Equal(t, string(original), string(raw))
				require.Equal(t, options, jobworkflow.TaskOptionsFor(td))
				return jobdb.NewTaskDataOrPanic(true), nil
			})
			require.NoError(t, err)
			require.Equal(t, tc.accept, ok)
			require.Equal(t, tc.accept, called)
		})
	}
}

type preparationEncodingJob struct {
	write           bool
	empty           bool
	prep            workerops.PreparationRequest
	changedInput    bool
	changedArtifact bool
}

func (preparationEncodingJob) Name() string { return "preparation-encoding" }
func (j preparationEncodingJob) Run(ctx jobworkflow.JobContext, _ jobdb.JobData) (jobdb.JobData, error) {
	value := "original"
	if j.changedInput {
		value = "changed"
	}
	pack := "original"
	if j.changedArtifact {
		pack = "changed"
	}
	req := workerops.ActivityInvocationRequest{Input: map[string]any{"text": value}, Preparation: j.prep}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if j.write && j.empty {
		raw = append([]byte(`{"preparation":{},`), raw[1:]...)
	}
	td := &jobdb.SimpleTaskData{Data: raw, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("input-pack", []byte(pack))}}
	var out jobdb.TaskData
	if j.write {
		out, err = ctx.DoTask(jobdb.RunPolicy{}, "probe:run", td)
	} else {
		out, err = newThinPackForwardingJobContext(ctx).DoTask(jobdb.RunPolicy{}, "probe:run", td)
	}
	if err != nil {
		return nil, err
	}
	// Prove recovery consumed the original ordinal exactly once.
	_, err = ctx.DoTask(jobdb.RunPolicy{}, "probe:run", jobdb.NewTaskDataOrPanic("next"))
	return out, err
}

func TestPreparationEncodingReplayWithJobDB(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		for _, encoding := range []string{"absent", "empty", "scopes", "extension"} {
			t.Run(backend+"/"+encoding, func(t *testing.T) {
				ctx := context.Background()
				db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
				var rt jobdb.WorkflowRuntime = db
				if backend == "http" {
					s := httptest.NewServer(remote.NewServer(db))
					t.Cleanup(s.Close)
					rt, err = remote.New(s.URL, s.Client())
					require.NoError(t, err)
				}
				old := preparationEncodingJob{write: true, empty: encoding == "empty"}
				if encoding == "scopes" {
					old.prep.Scopes = []toolenv.Scope{{ID: "scope", Packages: []string{"uv:black"}}}
				}
				if encoding == "extension" {
					old.prep.Extension = &workerops.ExtensionResolutionRequest{Selector: "nix:github:example/ops#probe"}
				}
				task := &artifactOrderTask{}
				engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("test").PlusWorkers(old, task).BuildEngine()
				require.NoError(t, err)
				key, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "test", JobType: old.Name(), Data: jobdb.NewTaskDataOrPanic(nil), RunPolicy: jobdb.DefaultRunPolicy()})
				require.NoError(t, err)
				runnable, err := jobworkflow.GetJobForRun(ctx, rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: old, TaskWorkers: []jobworkflow.TaskWorker{task}, WorkerID: "test", LeaseDuration: time.Minute})
				require.NoError(t, err)
				outcome, err := runnable.Run(nil)
				require.NoError(t, err)
				require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status)
				fixed := old
				fixed.write = false
				_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: fixed})
				require.NoError(t, err)
				for _, change := range []string{"input", "artifact", "preparation"} {
					altered := fixed
					switch change {
					case "input":
						altered.changedInput = true
					case "artifact":
						altered.changedArtifact = true
					case "preparation":
						altered.prep = workerops.PreparationRequest{Scopes: []toolenv.Scope{{ID: "changed", Packages: []string{"uv:ruff"}}}}
					}
					_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: altered})
					require.True(t, errors.Is(err, jobworkflow.ErrWorkflowNotDeterministic), "%s: %v", change, err)
				}
				require.EqualValues(t, 2, task.calls.Load(), "replay must not execute tasks")
			})
		}
	}
}

func TestEmptyPreparationRecoveryBothDirectionsWithArtifactOrder(t *testing.T) {
	for _, cachedEmpty := range []bool{false, true} {
		for _, permutation := range []bool{false, true} {
			req := workerops.ActivityInvocationRequest{Input: map[string]any{"run": "true"}, ArtifactKeys: []jobdb.ArtifactKey{
				{JobId: "source", TaskOrdinal: 1, Name: "a", SizeBytes: 1},
				{JobId: "source", TaskOrdinal: 1, Name: "b", SizeBytes: 1},
			}}
			cached, err := json.Marshal(req)
			require.NoError(t, err)
			if cachedEmpty {
				cached = append([]byte(`{"preparation":{},`), cached[1:]...)
			}
			if permutation {
				req.ArtifactKeys[0], req.ArtifactKeys[1] = req.ArtifactKeys[1], req.ArtifactKeys[0]
			}
			current, err := json.Marshal(req)
			require.NoError(t, err)
			if !cachedEmpty {
				current = append([]byte(`{"preparation":{},`), current[1:]...)
			}
			hash, err := legacyTaskInputHash(cached, nil)
			require.NoError(t, err)
			mismatch := jobworkflow.TaskInputMismatchError{TaskType: "probe:run", CachedInput: cached, CachedInputHash: hash, Meta: jobworkflow.TaskDeterminismMeta{TaskType: "probe:run"}}
			called := false
			_, ok, err := recoverEmptyPreparation("probe:run", &jobdb.SimpleTaskData{Data: current}, mismatch, func(data jobdb.TaskData) (jobdb.TaskData, error) {
				called = true
				raw, err := data.GetData()
				require.NoError(t, err)
				require.Equal(t, string(cached), string(raw))
				return jobdb.NewTaskDataOrPanic(true), nil
			})
			require.NoError(t, err)
			require.True(t, ok)
			require.True(t, called)
		}
	}
}
