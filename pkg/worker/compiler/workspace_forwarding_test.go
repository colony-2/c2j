package compiler

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceForwarderUsesScopeAndSkipsInternalTasks(t *testing.T) {
	forwarder := newThinPackForwardingJobContext(nil)
	forwarder.scopedMode = true
	pack := func(ordinal int64) jobdb.Artifact {
		art := jobdb.NewArtifactFromBytes(gitstate.ThinPackArtifactName, []byte("pack"))
		jobdb.AssignArtifactKey(art, jobdb.ArtifactKey{JobId: "job", TaskOrdinal: ordinal, Name: gitstate.ThinPackArtifactName, SizeBytes: 4})
		return art
	}
	a, b := pack(1), pack(2)
	call := func(scope string, want jobdb.Artifact, output ...jobdb.Artifact) {
		t.Helper()
		input := workerops.ActivityInvocationRequest{}
		if scope != "" {
			input.GitTaskContext.Workspace = &contextual.WorkspaceContext{ScopeID: scope}
		}
		_, err := forwarder.doTask(jobdb.RunPolicy{}, "probe:run", jobdb.NewTaskDataOrPanic(input), func(td jobdb.TaskData) (jobdb.TaskData, error) {
			raw, e := td.GetData()
			require.NoError(t, e)
			var got workerops.ActivityInvocationRequest
			require.NoError(t, json.Unmarshal(raw, &got))
			if want == nil {
				require.Nil(t, got.RestoreArtifact)
			} else {
				key, e := want.ArtifactKey()
				require.NoError(t, e)
				require.Equal(t, &key, got.RestoreArtifact)
			}
			return jobdb.NewTaskData(map[string]any{}, output...)
		})
		require.NoError(t, err)
	}
	diff := jobdb.NewArtifactFromBytes("diff_from_base.diff", []byte("root diff"))
	call("", nil, a, diff)
	call("B1", nil, b)
	call("", a)
	call("B1", b)
	call("B2", nil)
	user := jobdb.NewArtifactFromBytes("report.txt", []byte("report"))
	foreignDiff := jobdb.NewArtifactFromBytes("diff_from_base.diff", []byte("foreign diff"))
	require.Equal(t, []jobdb.Artifact{user, a, diff}, forwarder.resultArtifacts([]jobdb.Artifact{b, foreignDiff, user}))
	_, err := forwarder.doTask(jobdb.RunPolicy{}, WorkspaceResolutionTaskType, jobdb.NewTaskDataOrPanic(WorkspaceResolutionInput{Cell: "C"}), func(td jobdb.TaskData) (jobdb.TaskData, error) {
		arts, e := td.GetArtifacts()
		require.NoError(t, e)
		require.Empty(t, arts)
		return jobdb.NewTaskData(map[string]any{})
	})
	require.NoError(t, err)
	call("", a)
	call("B1", b)
	_, err = forwarder.doTask(jobdb.RunPolicy{}, "probe:run", jobdb.NewTaskDataOrPanic(workerops.ActivityInvocationRequest{}), func(jobdb.TaskData) (jobdb.TaskData, error) { return jobdb.NewTaskData(map[string]any{}, a, b) })
	require.ErrorContains(t, err, "ambiguous workspace snapshot")
}
