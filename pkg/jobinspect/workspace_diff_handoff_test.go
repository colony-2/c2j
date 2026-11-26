package jobinspect

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

const handoffJobID = "3McO3PDMSul8iEKaPoEg3vla7vH"

type handoffRuntime struct {
	frozenWorkspaceRuntime
	patch []byte
}

func (runtime *handoffRuntime) GetChapter(_ context.Context, ref jobdb.ChapterRef) (jobdb.Chapter, error) {
	if ref.JobKey != runtime.summary.JobKey || ref.Ordinal < 0 || ref.Ordinal >= int64(len(runtime.chapters)) {
		return jobdb.Chapter{}, jobdb.ErrJobNotFound
	}
	return runtime.chapters[ref.Ordinal], nil
}

func (runtime *handoffRuntime) OpenArtifact(_ context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	if ref.JobKey == runtime.summary.JobKey && ref.Ordinal == 28 && ref.Name == "diff_from_parent.diff" {
		return jobdb.NewArtifactFromBytes(ref.Name, runtime.patch), nil
	}
	return nil, jobdb.ErrJobNotFound
}

func openHandoffFixture(t *testing.T, name string) *gzip.Reader {
	t.Helper()
	file, err := os.Open("testdata/serial-handoff/" + name)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	return reader
}

func loadHandoffFixture(t *testing.T, edit func(int64, *jobdb.ExecutionRecord)) (*Service, map[int64]string, []byte) {
	t.Helper()
	var fixture struct {
		Start   json.RawMessage
		Records [][]jobdb.ExecutionRecord
	}
	require.NoError(t, json.NewDecoder(openHandoffFixture(t, "prefix.json.gz")).Decode(&fixture))
	require.Len(t, fixture.Records, 42)
	patch, err := io.ReadAll(openHandoffFixture(t, "ordinal-28.diff.gz"))
	require.NoError(t, err)
	key := jobdb.JobKey{TenantId: "c2", JobId: handoffJobID}
	runtime := &handoffRuntime{patch: patch}
	runtime.job = jobdb.JobInfo{Status: jobdb.JobStatusReady}
	runtime.summary = jobdb.JobSummary{JobKey: key, Cell: "cortex"}
	runtime.chapters = []jobdb.Chapter{{Body: jobdb.JobStartChapter{Input: jobdb.ApplicationInputBytes{Data: fixture.Start}}}}
	results := map[int64]string{}
	for index, records := range fixture.Records {
		ordinal := int64(index + 1)
		if edit != nil {
			for position := range records {
				edit(ordinal, &records[position])
			}
		}
		last := records[len(records)-1]
		body, err := json.Marshal(last)
		require.NoError(t, err)
		prefix, err := json.Marshal(records[:len(records)-1])
		require.NoError(t, err)
		chapter := jobdb.Chapter{
			Ordinal: ordinal, Body: jobdb.ExecutionChapter{Record: body},
			Metadata: jobdb.ChapterMetadata{Fields: map[string]jobdb.ChapterMetadataValue{
				"metadata": {Kind: jobdb.ChapterMetadataMap, Map: map[string]jobdb.ChapterMetadataValue{
					"__execution_prefix_v3": {Kind: jobdb.ChapterMetadataString, String: string(prefix)},
				}},
			}},
		}
		if last.Kind == "result" {
			results[chapter.Ordinal] = last.ID
		}
		if chapter.Ordinal == 28 {
			chapter.Artifacts = []jobdb.StoredArtifact{{Name: "diff_from_parent.diff", Size: 16277}}
		}
		runtime.chapters = append(runtime.chapters, chapter)
	}
	return &Service{Runtime: runtime, Tenant: key.TenantId}, results, patch
}

func TestWorkspaceDiffNativeSerialHandoff(t *testing.T) {
	service, results, stored := loadHandoffFixture(t, nil)
	require.Len(t, stored, 16277)
	require.Equal(t, "73f704efb7e7e84110c099efac8eabe9662ec825d2ea232d375ed18b8445d612", fmt.Sprintf("%x", sha256.Sum256(stored)))
	require.True(t, strings.HasSuffix(results[42], `/scope:"op:story-review-tests"#1/attempt:1/task:"command_execution:command_execution"#1/attempt:1`))
	require.True(t, strings.HasSuffix(results[28], `/scope:"op:squash"#1/attempt:1/task:"squash:squash"#1/attempt:1`))
	main := "main"
	for name, selector := range map[string]*string{"default": nil, "main": &main} {
		t.Run(name, func(t *testing.T) {
			metadata, body, err := service.OpenWorkspaceDiff(t.Context(), WorkspaceDiffRequest{JobID: handoffJobID, WorkspaceDiffOptions: WorkspaceDiffOptions{Workspace: selector}})
			require.NoError(t, err, "cause: %v", errors.Unwrap(err))
			require.Equal(t, "available", metadata.Status, "%v", metadata.Reasons)
			require.Equal(t, strings.TrimSuffix(results[41], "/attempt:1"), metadata.StateProducer.Task)
			require.NotNil(t, metadata.Source)
			require.Equal(t, strings.TrimSuffix(results[28], "/attempt:1"), metadata.Source.Producer.Task)
			require.Equal(t, 1, metadata.Source.Producer.Attempt)
			require.Equal(t, jobdb.ArtifactKey{JobId: handoffJobID, TaskOrdinal: 28, Name: "diff_from_parent.diff", SizeBytes: 16277}, metadata.Source.Key)
			got, err := io.ReadAll(body)
			require.NoError(t, err)
			require.NoError(t, body.Close())
			require.Equal(t, stored, got)
		})
	}
}

func TestWorkspaceDiffNativeSerialHandoffRejections(t *testing.T) {
	replaceIncoming := func(edit func(*coretask.WorkspaceState)) func(*jobdb.ExecutionRecord) {
		return func(record *jobdb.ExecutionRecord) {
			editHandoffWorkspace(t, record, "workspace_before", edit)
		}
	}
	for _, test := range []struct {
		name string
		edit func(int64, *jobdb.ExecutionRecord)
	}{
		{"matching-endpoints-without-serial-proof", func(_ int64, record *jobdb.ExecutionRecord) {
			record.ID = strings.ReplaceAll(record.ID, `/scope:"state_machine:adaptive3-execute"#1`, `/scope:"parallel:adaptive3-execute"#1`)
		}},
		{"divergent-siblings", func(ordinal int64, record *jobdb.ExecutionRecord) {
			if ordinal == 17 && record.Kind == "result" {
				editHandoffWorkspace(t, record, "workspace_after", func(state *coretask.WorkspaceState) {
					state.HeadCommit = strings.Repeat("a", 40)
				})
			}
		}},
		{"mismatched-repository", handoffFirstEdit(replaceIncoming(func(state *coretask.WorkspaceState) { state.Repository = "https://example.com/other.git" }))},
		{"mismatched-workspace", handoffFirstEdit(replaceIncoming(func(state *coretask.WorkspaceState) { state.ScopeID = "other" }))},
		{"mismatched-cell", handoffFirstEdit(replaceIncoming(func(state *coretask.WorkspaceState) { state.Cell = "other" }))},
		{"mismatched-base", handoffFirstEdit(replaceIncoming(func(state *coretask.WorkspaceState) { state.BaseCommit = strings.Repeat("a", 40) }))},
		{"mismatched-head", handoffFirstEdit(replaceIncoming(func(state *coretask.WorkspaceState) { state.HeadCommit = strings.Repeat("a", 40) }))},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := loadHandoffFixture(t, test.edit)
			main := "main"
			for _, selector := range []*string{nil, &main} {
				metadata, body, err := service.OpenWorkspaceDiff(t.Context(), WorkspaceDiffRequest{JobID: handoffJobID, WorkspaceDiffOptions: WorkspaceDiffOptions{Workspace: selector}})
				require.NoError(t, err, "cause: %v", errors.Unwrap(err))
				require.Equal(t, "unavailable", metadata.Status)
				require.Equal(t, []string{"workspace adoption between branches is unproven"}, metadata.Reasons)
				require.Nil(t, body)
			}
		})
	}
}

func handoffFirstEdit(edit func(*jobdb.ExecutionRecord)) func(int64, *jobdb.ExecutionRecord) {
	return func(ordinal int64, record *jobdb.ExecutionRecord) {
		if ordinal == 18 && record.Kind == "result" {
			edit(record)
		}
	}
}

func editHandoffWorkspace(t *testing.T, record *jobdb.ExecutionRecord, field string, edit func(*coretask.WorkspaceState)) {
	t.Helper()
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(record.Output.Data, &envelope))
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope["payload"], &payload))
	var state coretask.WorkspaceState
	require.NoError(t, json.Unmarshal(payload[field], &state))
	edit(&state)
	marshal := func(value any) json.RawMessage {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		return raw
	}
	payload[field] = marshal(state)
	envelope["payload"] = marshal(payload)
	record.Output.Data = marshal(envelope)
}
