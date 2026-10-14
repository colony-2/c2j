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
}
