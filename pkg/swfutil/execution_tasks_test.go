package swfutil

import (
	"encoding/json"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExecutionTasksKeepsBlockInvocationsAndTaskAttemptsDistinct(t *testing.T) {
	descriptor := json.RawMessage(`{"call":{"task_type":"probe:run"}}`)
	key := &jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 19, Name: "stored-stderr", SizeBytes: 4}
	first := `/scope:"block"/attempt:1/task:"probe"#1`
	second := `/scope:"block"/attempt:2/task:"probe"#1`
	run := jobdb.GetJobRunResponse{Execution: []jobdb.ExecutionHistoryEntry{
		{Ordinal: 1, Record: jobdb.ExecutionRecord{Kind: "scope", ID: "block"}},
		{Ordinal: 2, Record: jobdb.ExecutionRecord{Kind: "task", ID: first, Descriptor: descriptor}},
		{Ordinal: 19, Record: jobdb.ExecutionRecord{Kind: "result", ID: first + "/attempt:1", ErrorKind: "app", Output: &jobdb.ExecutionSnapshot{Data: json.RawMessage(`{"diagnostic":"failed"}`), Artifacts: []jobdb.ExecutionArtifact{{Name: "stderr.txt", Key: key}}}}},
		{Ordinal: 25, Record: jobdb.ExecutionRecord{Kind: "result", ID: first + "/attempt:2", Output: &jobdb.ExecutionSnapshot{Data: json.RawMessage(`{"ok":true}`)}}},
		{Ordinal: 27, Record: jobdb.ExecutionRecord{Kind: "task", ID: second, Descriptor: descriptor}},
		{Ordinal: 40, Record: jobdb.ExecutionRecord{Kind: "result", ID: second + "/attempt:1"}},
		{Ordinal: 41, Record: jobdb.ExecutionRecord{Kind: "result", ID: "block/attempt:2"}},
	}}
	tasks, err := ExecutionTasks(run)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	require.Equal(t, first, tasks[0].InvocationID)
	require.Equal(t, second, tasks[1].InvocationID)
	require.Len(t, tasks[0].Attempts, 2)
	require.Len(t, tasks[1].Attempts, 1)
	require.Equal(t, int64(19), tasks[0].Attempts[0].Ordinal)
	require.Equal(t, jobdb.TaskAttemptStateFailed, tasks[0].Attempts[0].State)
	require.Equal(t, jobdb.TaskAttemptStateSucceeded, tasks[0].Attempts[1].State)
	require.JSONEq(t, `{"diagnostic":"failed"}`, string(tasks[0].Attempts[0].Output.Data))
	require.Equal(t, "stderr.txt", tasks[0].Attempts[0].Output.Artifacts[0].Name)
	require.Equal(t, key, tasks[0].Attempts[0].Output.Artifacts[0].Key)
	require.Equal(t, int64(4), tasks[0].Attempts[0].Output.Artifacts[0].SizeBytes)
	require.Nil(t, tasks[0].Attempts[0].Input)
	require.Nil(t, tasks[1].Attempts[0].Output)
}

func TestExecutionTasksRequiresStoredArtifactReferences(t *testing.T) {
	for _, direction := range []string{"input", "output"} {
		for _, tc := range []struct {
			name string
			key  *jobdb.ArtifactKey
			err  string
		}{
			{name: "missing_key", err: "has no stored key"},
			{name: "invalid_key", key: &jobdb.ArtifactKey{Name: "stored", SizeBytes: 4}, err: "jobId cannot be empty"},
			{name: "unknown_size", key: &jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 2, Name: "stored", SizeBytes: -1}, err: "has unknown stored size"},
			{name: "zero_bytes", key: &jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 2, Name: "stored", SizeBytes: 0}},
		} {
			t.Run(direction+"/"+tc.name, func(t *testing.T) {
				snapshot := &jobdb.ExecutionSnapshot{
					Data:      json.RawMessage(`{"payload":"preserved"}`),
					Artifacts: []jobdb.ExecutionArtifact{{Name: "logical", Key: tc.key}},
				}
				result := jobdb.ExecutionRecord{Kind: "result", ID: "probe/attempt:1"}
				if direction == "input" {
					result.Input = snapshot
				} else {
					result.Output = snapshot
				}
				run := jobdb.GetJobRunResponse{Execution: []jobdb.ExecutionHistoryEntry{
					{Ordinal: 1, Record: jobdb.ExecutionRecord{Kind: "task", ID: "probe", Descriptor: json.RawMessage(`{"call":{"task_type":"probe:run"}}`)}},
					{Ordinal: 2, Record: result},
				}}
				tasks, err := ExecutionTasks(run)
				if tc.err != "" {
					require.ErrorContains(t, err, "task attempt probe/attempt:1 "+direction)
					require.ErrorContains(t, err, `execution artifact "logical"`)
					require.ErrorContains(t, err, tc.err)
					require.Nil(t, tasks)
					return
				}
				require.NoError(t, err)
				require.Len(t, tasks, 1)
				require.Len(t, tasks[0].Attempts, 1)
				io := tasks[0].Attempts[0].Output
				if direction == "input" {
					io = tasks[0].Attempts[0].Input
				}
				require.NotNil(t, io)
				require.JSONEq(t, string(snapshot.Data), string(io.Data))
				require.Equal(t, []jobdb.ArtifactInfo{{Name: "logical", SizeBytes: 0, Key: tc.key}}, io.Artifacts)
			})
		}
	}
}
