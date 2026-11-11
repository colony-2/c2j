package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	workflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestFinalManifestStatesAndMembership(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		state model.FinalManifestState
		count int
	}{
		{`{}`, model.FinalManifestAbsent, 0},
		{`{"artifact_refs":{}}`, model.FinalManifestEmpty, 0},
		{`{"artifact_refs":null}`, model.FinalManifestNull, 0},
		{`{"artifact_refs":[]}`, model.FinalManifestUnsupported, 0},
		{`{"artifact_refs":{"unknown":{"kind":"future"}}}`, model.FinalManifestUnsupported, 1},
		{`{"artifact_refs":{"bad":null,"good":{"kind":"external","name":"link","external":{"url":"https://must-not-fetch.invalid"}}}}`, model.FinalManifestMalformed, 2},
		{`{"files":[{"name":"intermediate"}],"artifact_refs":{"only":{"kind":"attached","name":"missing"}}}`, model.FinalManifestPresent, 1},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			a := jobdb.JobAttempt{Ordinal: 19, Output: &jobdb.TaskIO{Data: []byte(tc.raw), Artifacts: []jobdb.ArtifactInfo{{Name: "input.md"}}}}
			got := finalDocuments("job", &a)
			require.Equal(t, tc.state, got.State)
			require.Len(t, got.Documents, tc.count)
			require.EqualValues(t, 19, *got.AttemptOrdinal)
		})
	}
	stored := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "grandchild", TaskOrdinal: 8, Name: "storage.md", SizeBytes: 3})
	raw, err := json.Marshal(map[string]any{"artifact_refs": map[string]any{"display.md": stored, "attached alias": artifacts.Ref{Kind: artifacts.RefKindAttached, Name: "recorded.md"}}})
	require.NoError(t, err)
	a := jobdb.JobAttempt{Output: &jobdb.TaskIO{Data: raw, Artifacts: []jobdb.ArtifactInfo{{Name: "recorded.md", Key: &stored.Stored.Key}, {Name: "intermediate.md"}}}}
	got := finalDocuments("parent", &a)
	require.Len(t, got.Documents, 2)
	for _, d := range got.Documents {
		require.Equal(t, "stored", d.Resolution)
		require.Equal(t, "grandchild", d.Producer.JobId)
		require.Equal(t, "storage.md", d.Producer.Name)
		require.NotEqual(t, d.Alias, d.Producer.Name)
	}
	got = finalDocuments("parent", &jobdb.JobAttempt{Output: &jobdb.TaskIO{Data: []byte(`{"artifact_refs":{"value":{"kind":"value","format":"json","value":{"n":1}}}}`)}})
	require.Equal(t, "value", got.Documents[0].Resolution)
	require.Nil(t, got.Documents[0].Producer)
}

func TestFinalAttachmentCyclesAndMissingMaps(t *testing.T) {
	attached := func(name string) artifacts.Ref { return artifacts.Ref{Kind: artifacts.RefKindAttached, Name: name} }
	for _, refs := range []map[string]artifacts.Ref{nil, {"a": attached("b"), "b": attached("a")}, {"a": {Kind: artifacts.RefKindStored}}} {
		r, reason := resolveFinalRef(attached("a"), refs)
		require.True(t, r.IsZero())
		require.NotEmpty(t, reason)
	}
}

type documentErrorEngine struct {
	outcomeEngine
	getErr, readErr error
	keys            []jobdb.ArtifactKey
}

func (e *documentErrorEngine) GetArtifact(_ string, k jobdb.ArtifactKey) (jobdb.Artifact, error) {
	e.keys = append(e.keys, k)
	if e.getErr != nil {
		return nil, e.getErr
	}
	if e.readErr != nil {
		return failingDocumentArtifact{Artifact: jobdb.NewArtifactFromBytes(k.Name, nil), err: e.readErr}, nil
	}
	return jobdb.NewArtifactFromBytes(k.Name, []byte("bytes")), nil
}

type failingDocumentArtifact struct {
	jobdb.Artifact
	err error
}

func (a failingDocumentArtifact) Bytes(context.Context) ([]byte, error) { return nil, a.err }

func TestFinalDocumentSelectionAndReadErrors(t *testing.T) {
	ctx := celltest.Context(context.Background())
	ref := artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 42, Name: "original.md", SizeBytes: 5})
	raw, err := json.Marshal(map[string]any{"artifact_refs": map[string]any{"alias": ref}})
	require.NoError(t, err)
	prior := jobdb.JobAttempt{Ordinal: 1, Output: &jobdb.TaskIO{Data: raw}}
	latest := jobdb.JobAttempt{Ordinal: 2, Output: &jobdb.TaskIO{Data: []byte(`{"artifact_refs":{}}`)}}
	e := &documentErrorEngine{outcomeEngine: outcomeEngine{run: jobdb.GetJobRunResponse{Job: jobdb.JobRunSummary{Status: jobdb.JobStatusCompleted}, Attempts: []jobdb.JobAttempt{prior, latest}}}}
	s, err := New(Config{Engine: e})
	require.NoError(t, err)
	got, err := s.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Equal(t, model.FinalManifestEmpty, got.State)
	e.run.Attempts = []jobdb.JobAttempt{prior}
	got, err = s.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Len(t, got.Documents, 1)
	require.Empty(t, e.keys, "projection must not probe byte availability")
	for _, failure := range []struct {
		name   string
		lookup bool
	}{{"denied", true}, {"storage open", true}, {"storage read", false}, {"missing bytes", false}, {"backend reports expired", false}} {
		t.Run(failure.name, func(t *testing.T) {
			cause := errors.New(failure.name)
			e.getErr = nil
			e.readErr = nil
			if failure.lookup {
				e.getErr = cause
			} else {
				e.readErr = cause
			}
			_, err = s.GetFinalDocument(ctx, model.GetFinalDocumentRequest{ProjectID: "tenant", JobID: "job", Alias: "alias"})
			require.ErrorIs(t, err, cause)
			key := e.keys[len(e.keys)-1]
			require.Equal(t, "producer", key.JobId)
			require.EqualValues(t, 42, key.TaskOrdinal)
			require.Equal(t, "original.md", key.Name)
			retained, err := s.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: "job"})
			require.NoError(t, err)
			require.Len(t, retained.Documents, 1)
			require.Equal(t, "stored", retained.Documents[0].Resolution)
		})
	}
	e.run.Job.Status = jobdb.JobStatusActive
	_, err = s.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: "job"})
	require.ErrorIs(t, err, ErrOutcomePending)
}

func TestFinalDocumentsSQLiteForwardedProducer(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "documents.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	var previous jobdb.Artifact
	var original jobdb.ArtifactKey
	for i, job := range []string{"grandchild", "child", "parent"} {
		alias := fmt.Sprintf("alias-%d.md", i)
		worker := workflow.ExecutionWorker{JobType: "documents", Execute: func(c workflow.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
			ref := artifacts.Ref{Kind: artifacts.RefKindAttached, Name: "storage.md"}
			if previous != nil {
				key, err := previous.ArtifactKey()
				if err != nil {
					return nil, err
				}
				// Production forwarding carries the stored ref in the manifest.
				return jobdb.NewTaskDataOrPanic(map[string]any{"artifact_refs": map[string]artifacts.Ref{alias: artifacts.NewStoredRef(key)}}), nil
			}
			return jobdb.NewTaskDataOrPanic(map[string]any{"artifact_refs": map[string]artifacts.Ref{alias: ref}}, jobdb.NewArtifactFromBytes("storage.md", []byte("original bytes"))), nil
		}}
		handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: job, JobType: "documents", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
		require.NoError(t, err)
		runner, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: handle.JobKey, JobWorker: worker, WorkerID: "documents", LeaseDuration: time.Minute})
		require.NoError(t, err)
		_, err = runner.Run(nil)
		require.NoError(t, err)
		s, err := New(Config{Engine: engine})
		require.NoError(t, err)
		manifest, err := s.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: job})
		require.NoError(t, err)
		require.Equal(t, model.FinalManifestPresent, manifest.State)
		require.Len(t, manifest.Documents, 1)
		member := manifest.Documents[0]
		require.Equal(t, alias, member.Alias)
		require.NotNil(t, member.Producer, "%+v", member)
		if i == 0 {
			original = *member.Producer
		} else {
			require.Equal(t, original, *member.Producer)
		}
		require.Equal(t, "grandchild", member.Producer.JobId)
		require.Equal(t, "storage.md", member.Producer.Name)
		data, err := s.GetFinalDocument(ctx, model.GetFinalDocumentRequest{ProjectID: "tenant", JobID: job, Alias: alias})
		require.NoError(t, err)
		require.Equal(t, "original bytes", string(data.Content))
		for _, onRead := range []bool{false, true} {
			cause := errors.New("injected producer transport failure")
			guarded, err := New(Config{Engine: finalTransportEngine{Engine: engine, cause: cause, onRead: onRead}})
			require.NoError(t, err)
			retained, err := guarded.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: job})
			require.NoError(t, err)
			require.Len(t, retained.Documents, 1)
			_, err = guarded.GetFinalDocument(ctx, model.GetFinalDocumentRequest{ProjectID: "tenant", JobID: job, Alias: alias})
			require.ErrorIs(t, err, cause)
		}
		previous = member.Producer.ToLazyArtifact(engine, "tenant")
	}
}

type finalTransportEngine struct {
	workflow.Engine
	cause  error
	onRead bool
}

func (e finalTransportEngine) GetArtifact(tenant string, key jobdb.ArtifactKey) (jobdb.Artifact, error) {
	if !e.onRead {
		return nil, e.cause
	}
	artifact, err := e.Engine.GetArtifact(tenant, key)
	if err != nil {
		return nil, err
	}
	return failingDocumentArtifact{Artifact: artifact, err: e.cause}, nil
}

func TestFinalDocumentsSQLiteMissingBytesKeepsMembership(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "missing.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	engine, err := workflow.NewEngineBuilder().WithRuntime(rt).BuildEngine()
	require.NoError(t, err)
	missing := jobdb.ArtifactKey{JobId: "absent-producer", TaskOrdinal: 100, Name: "missing.md", SizeBytes: 1}
	worker := workflow.ExecutionWorker{JobType: "missing", Execute: func(workflow.ExecutionContext, jobdb.JobData) (jobdb.JobData, error) {
		return jobdb.NewTaskDataOrPanic(map[string]any{"artifact_refs": map[string]artifacts.Ref{"retained-member": artifacts.NewStoredRef(missing)}}), nil
	}}
	h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "job", JobType: "missing", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	runner, err := workflow.GetJobForRun(ctx, rt, workflow.GetJobForRunRequest{JobKey: h.JobKey, JobWorker: worker, WorkerID: "missing", LeaseDuration: time.Minute})
	require.NoError(t, err)
	_, err = runner.Run(nil)
	require.NoError(t, err)
	s, err := New(Config{Engine: engine})
	require.NoError(t, err)
	_, err = s.GetFinalDocument(ctx, model.GetFinalDocumentRequest{ProjectID: "tenant", JobID: "job", Alias: "retained-member"})
	require.Error(t, err)
	retained, err := s.GetFinalDocuments(ctx, model.GetWorkflowOutcomeRequest{ProjectID: "tenant", JobID: "job"})
	require.NoError(t, err)
	require.Len(t, retained.Documents, 1)
	require.Equal(t, missing, *retained.Documents[0].Producer)
	require.NotContains(t, retained.Documents[0].Diagnostic, "expired")
}
