package live

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/pkg/story/internal/inbox"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestInboxRecordedSelections(t *testing.T) {
	td := &jobdb.SimpleTaskData{Data: []byte(`{"input":{"nested":[{"kind":"value","name":"typed.json","format":"json","value":9007199254740993}]},"context":{},"artifacts":{"z/":{"kind":"stored","stored":{"key":{"jobId":"source","taskOrdinal":42,"name":"original.txt"}}},"a/renamed":{"kind":"stored","stored":{"key":{"jobId":"source","taskOrdinal":42,"name":"original.txt"}}},"typed.json":{"kind":"value","name":"typed.json","format":"json","value":9007199254740993},"remote/":{"kind":"external","name":"archive","external":{"url":"https://user:secret@example.test/a?token=credential","expand":true}},"bad":{"kind":"future"}}}`)}
	// ArtifactKey uses its public JSON field tags, so build the exact key fixture.
	key := jobdb.ArtifactKey{JobId: "source", TaskOrdinal: 42, Name: "original.txt"}
	got := inbox.Project(context.Background(), td)
	require.Equal(t, "available", got.Status)
	require.Len(t, got.Entries, 5)
	require.Equal(t, []string{"a/renamed", "bad", "remote/", "typed.json", "z/"}, []string{got.Entries[0].Binding, got.Entries[1].Binding, got.Entries[2].Binding, got.Entries[3].Binding, got.Entries[4].Binding})
	require.Equal(t, &key, got.Entries[0].SourceKey)
	require.Equal(t, "z/original.txt", got.Entries[4].Path)
	require.Empty(t, got.Entries[2].Path)
	require.Equal(t, "member_inventory_unavailable", got.Entries[2].Reason)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "credential")
	require.NotContains(t, string(raw), "9007199254740993")
	refs, err := inbox.Decode(context.Background(), td)
	require.NoError(t, err)
	data, err := refs["typed.json"].ValueBytes()
	require.NoError(t, err)
	require.Equal(t, "9007199254740993\n", string(data))
}
func TestInboxHistoryRecognition(t *testing.T) {
	for _, tc := range []struct{ raw, status, reason string }{
		{`{"input":null,"context":{}}`, "available", ""},
		{`{}`, "unavailable", "history_unsupported"},
		{`{"unrelated":[]}`, "unavailable", "history_unsupported"},
		{`{`, "unavailable", "history_malformed"},
		{`{"input":4,"context":{}}`, "unavailable", "history_malformed"},
		{`{"input":null,"context":{},"input_artifact":"private"}`, "unavailable", "attachment_missing"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := inbox.Project(context.Background(), &jobdb.SimpleTaskData{Data: []byte(tc.raw)})
			require.Equal(t, tc.status, got.Status)
			require.Equal(t, tc.reason, got.Reason)
			require.Empty(t, got.Entries)
		})
	}
	require.Equal(t, "history_missing", inbox.Project(context.Background(), nil).Reason)
	require.Equal(t, "history_unreadable", inbox.Project(context.Background(), deniedData{}).Reason)
	for _, kind := range []model.JobRunStoryNodeKind{model.JobRunStoryNodeKindRecipe, model.JobRunStoryNodeKindSequence, model.JobRunStoryNodeKindOp, model.JobRunStoryNodeKindRecipeSourceResolution} {
		require.Equal(t, "not_applicable", defaultInbox(kind).Status)
	}
	require.Equal(t, "pending_input", defaultInbox(model.JobRunStoryNodeKindOpStep).Reason)
}

type deniedData struct{ jobdb.TaskData }

func (deniedData) GetData() (jobdb.Data, error) { return nil, errors.New("denied private payload") }
func TestInboxAttemptCopies(t *testing.T) {
	src := &model.JobRunStoryNode{Inbox: &model.Inbox{Status: "available", Entries: []model.InboxEntry{{Binding: "original", SourceKey: &jobdb.ArtifactKey{JobId: "source"}}}}}
	promoted := &model.JobRunStoryNode{}
	copyAttemptIntoStep(promoted, src)
	snapshot := cloneNode(src)
	promoted.Inbox.Entries[0].Binding = "changed"
	promoted.Inbox.Entries[0].SourceKey.JobId = "changed"
	snapshot.Inbox.Entries[0].SourceKey.JobId = "snapshot"
	require.Equal(t, "source", src.Inbox.Entries[0].SourceKey.JobId)
	require.Equal(t, "original", src.Inbox.Entries[0].Binding)
}
func TestInboxPrivateMetadataAndPaths(t *testing.T) {
	payload := []byte(`{"input":null,"artifacts":{"nested/":{"kind":"value","name":"x.txt","format":"text","value":"${{ literal }}"}}}`)
	inline := &jobdb.SimpleTaskData{Data: append([]byte(`{"context":{},`), payload[1:]...)}
	private := &jobdb.SimpleTaskData{Data: []byte(`{"context":{},"input":null,"input_artifact":"private"}`), Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("private", payload)}}
	require.Equal(t, inbox.Project(context.Background(), inline), inbox.Project(context.Background(), private))
	private.Artifacts = []jobdb.Artifact{jobdb.NewArtifactFromBytes("private", []byte(`{}`))}
	require.Equal(t, "attachment_malformed", inbox.Project(context.Background(), private).Reason)
	for _, tc := range []struct{ binding, name, want string }{{"a//b/", "c.txt", "a/b/c.txt"}, {"../escape", "x", ""}, {"/abs", "x", ""}, {"folder/", "../escape", "escape"}, {"folder/", "../../escape", ""}, {"nested/name", "x", "nested/name"}} {
		require.Equal(t, tc.want, inbox.Destination(tc.binding, tc.name))
	}
}

func TestInboxProjectionDoesNotReadSources(t *testing.T) {
	td := &noArtifactReadData{SimpleTaskData: jobdb.SimpleTaskData{Data: []byte(`{"context":{},"input":null,"artifacts":{"stored":{"kind":"stored","stored":{"key":{"jobId":"other","taskOrdinal":73,"name":"file"}}},"value":{"kind":"value","format":"text","value":null}}}`)}}
	got := inbox.Project(context.Background(), td)
	require.Equal(t, "available", got.Status)
	require.Equal(t, "stored", got.Entries[0].Content)
	require.Equal(t, "descriptor_malformed", got.Entries[1].Reason)
	// Unsupported value formats retain binding metadata without attempting bytes.
	td.Data = []byte(`{"context":{},"input":null,"artifacts":{"value":{"kind":"value","format":"future","value":{}}}}`)
	require.Equal(t, "format_unsupported", inbox.Project(context.Background(), td).Entries[0].Reason)
}

type noArtifactReadData struct{ jobdb.SimpleTaskData }

func (noArtifactReadData) GetArtifacts() ([]jobdb.Artifact, error) {
	panic("inventory must not read stored source bytes")
}

func TestInboxSyntheticSourceNeverDecodesBindings(t *testing.T) {
	r := NewRecorder(Options{})
	r.OnJobStart(jobworkflow.JobStartEvent{AttemptNumber: 1})
	r.OnTaskStart(jobworkflow.TaskStartEvent{InvocationID: "source/attempt:1", TaskType: compiler.RootSourceResolutionTaskType, AttemptNumber: 1, Input: &jobdb.SimpleTaskData{Data: []byte(`{"context":{},"input":{},"artifacts":{"bad":{"kind":"value","format":"text","value":"irrelevant"}}}`)}})
	require.Equal(t, "not_applicable", r.sourceNodes[1].node.Inbox.Status)
	require.Empty(t, r.sourceNodes[1].node.Inbox.Entries)
}
