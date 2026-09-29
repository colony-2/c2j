package compiler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestArtifactOrderComparisonDoesNotHideInputChanges(t *testing.T) {
	original := []byte(`{"input":{"large":9007199254740993,"text":"original"},"artifact_keys":[{"jobId":"source","taskOrdinal":1,"name":"b","sizeBytes":2},{"jobId":"source","taskOrdinal":1,"name":"a","sizeBytes":1}],"restore_artifact":{"name":"pack"}}`)
	want, err := canonicalArtifactOrder(original)
	require.NoError(t, err)
	for _, tt := range []struct {
		name, raw string
		equal     bool
	}{
		{"permutation", `{"restore_artifact":{"name":"pack"},"artifact_keys":[{"name":"a","sizeBytes":1,"jobId":"source","taskOrdinal":1},{"name":"b","sizeBytes":2,"jobId":"source","taskOrdinal":1}],"input":{"text":"original","large":9007199254740993}}`, true},
		{"changed input", `{"input":{"large":9007199254740993,"text":"changed"},"artifact_keys":[{"jobId":"source","taskOrdinal":1,"name":"b","sizeBytes":2},{"jobId":"source","taskOrdinal":1,"name":"a","sizeBytes":1}],"restore_artifact":{"name":"pack"}}`, false},
		{"rounded number", `{"input":{"large":9007199254740992,"text":"original"},"artifact_keys":[{"jobId":"source","taskOrdinal":1,"name":"b","sizeBytes":2},{"jobId":"source","taskOrdinal":1,"name":"a","sizeBytes":1}],"restore_artifact":{"name":"pack"}}`, false},
		{"duplicate key", `{"input":{"large":9007199254740993,"text":"original"},"artifact_keys":[{"jobId":"source","taskOrdinal":1,"name":"a","sizeBytes":1},{"jobId":"source","taskOrdinal":1,"name":"a","sizeBytes":1}],"restore_artifact":{"name":"pack"}}`, false},
		{"changed restore", `{"input":{"large":9007199254740993,"text":"original"},"artifact_keys":[{"jobId":"source","taskOrdinal":1,"name":"b","sizeBytes":2},{"jobId":"source","taskOrdinal":1,"name":"a","sizeBytes":1}],"restore_artifact":{"name":"different"}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := canonicalArtifactOrder([]byte(tt.raw))
			require.NoError(t, err)
			require.Equal(t, tt.equal, string(want) == string(got))
		})
	}
}

type artifactOrderJob struct {
	legacy   bool
	history  taskHistoryReader
	pack     string
	input    string
	observed *error
}

func (artifactOrderJob) Name() string { return "artifact-order" }
func (j artifactOrderJob) Run(ctx jobworkflow.JobContext, _ jobdb.JobData) (jobdb.JobData, error) {
	a := jobdb.ArtifactKey{JobId: "source", TaskOrdinal: 1, Name: "a", SizeBytes: 1}
	b := jobdb.ArtifactKey{JobId: "source", TaskOrdinal: 1, Name: "b", SizeBytes: 1}
	keys := []jobdb.ArtifactKey{a, b}
	if j.legacy {
		keys = []jobdb.ArtifactKey{b, a}
	}
	req := workerops.ActivityInvocationRequest{Input: map[string]any{"text": j.input}, ArtifactKeys: keys}
	td := jobdb.NewTaskDataOrPanic(req, jobdb.NewArtifactFromBytes("input-pack", []byte(j.pack)))
	if j.legacy {
		return ctx.DoTask(jobdb.RunPolicy{}, "probe:run", td)
	}
	forwarder := newThinPackForwardingJobContext(ctx)
	forwarder.history = j.history
	out, err := forwarder.DoTask(jobdb.RunPolicy{}, "probe:run", td)
	if j.observed != nil {
		*j.observed = err
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

type artifactOrderTask struct{ calls atomic.Int32 }

func (*artifactOrderTask) Name() string { return "probe:run" }
func (t *artifactOrderTask) Run(jobworkflow.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
	t.calls.Add(1)
	return jobdb.NewTaskData(map[string]any{"ok": true}, jobdb.NewArtifactFromBytes(gitstate.ThinPackArtifactName, []byte("snapshot")))
}

func TestReplayLegacyArtifactOrderWithRealJobDB(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rt := toyruntime.New()
	task := &artifactOrderTask{}
	old := artifactOrderJob{legacy: true, pack: "original", input: "original"}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("tenant").PlusWorkers(old, task).BuildEngine()
	require.NoError(t, err)
	go engine.Run(ctx)
	key, err := engine.SubmitJob(ctx, jobdb.SubmitJob{TenantId: "tenant", JobType: old.Name(), Data: jobdb.NewTaskDataOrPanic(map[string]any{}), RunPolicy: jobdb.DefaultRunPolicy()})
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 5*time.Second, key, engine))
	require.EqualValues(t, 1, task.calls.Load())
	observedSuccess := errors.New("replay did not execute the job worker")
	fixed := artifactOrderJob{observed: &observedSuccess, history: &workerworkflow.SWFWorkflowControl{Engine: engine}, pack: "original", input: "original"}
	out, err := engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: fixed})
	require.NoError(t, err)
	require.NoError(t, observedSuccess)
	raw, err := out.GetData()
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true}`, string(raw))
	artifacts, err := out.GetArtifacts()
	require.NoError(t, err)
	require.Len(t, artifacts, 1)
	bytes, err := artifacts[0].Bytes(ctx)
	require.NoError(t, err)
	require.Equal(t, "snapshot", string(bytes))
	for _, change := range []string{"pack", "input"} {
		altered := fixed
		if change == "pack" {
			altered.pack = "changed"
		} else {
			altered.input = "changed"
		}
		var observed error
		altered.observed = &observed
		// JobDB may return the cached job result after an earlier task error;
		// assert the compiler rejects the changed task before that outer layer.
		_, _ = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: altered})
		require.ErrorIs(t, observed, jobworkflow.ErrWorkflowNotDeterministic, change)
	}
	require.EqualValues(t, 1, task.calls.Load(), "replay must never re-execute the task")
}
