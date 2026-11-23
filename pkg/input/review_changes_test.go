package input

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	c2workflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// These are ordinary existing input tasks in the same job as real writer
// bundles. Changes has no form fields, receipt attribution or write lifecycle.
func TestChangesJ40J42L04LegacyReviewCompatibility(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, err := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "review.db"), Projects: jobdbcelltest.UnrestrictedProjects{}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.ConsumerRuntime = db
			if backend == "http" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			celltest.Create(t, rt, "tenant", "changes", "example.test/changes")
			original := jobdb.NewArtifactFromBytes("design.md", []byte("original child document\n"))
			producer, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobType: "source", Cell: "changes", CellRepo: "example.test/changes", Data: jobdb.NewTaskDataOrPanic(nil, original)}})
			require.NoError(t, err)
			stored, err := jobdb.GetJobRun(ctx, rt, jobdb.GetJobRunRequest{JobKey: producer.JobKey, IncludeInputs: true, IncludeArtifacts: true})
			require.NoError(t, err)
			require.Len(t, stored.Start.Input.Artifacts, 1)
			key := *stored.Start.Input.Artifacts[0].Key
			ref := artifacts.NewStoredRef(key)
			engine, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			ctl := &c2workflow.SWFWorkflowControl{Engine: engine}
			inputs, err := NewRuntime(ctl, nil)
			require.NoError(t, err)
			deps := coreops.NewOpDependenciesBuilder().WithWorkflowControl(&clientControl{artifact: func(_ string, k jobdb.ArtifactKey) jobdb.Artifact { return ctl.GetArtifactLazy(ctx, "tenant", k) }}).Build()
			cfg := reviewConfig()
			cfg.Documents = map[string]any{"design": ref}
			first, err := buildForm(deps, ctx, Input{Form: cfg})
			require.NoError(t, err)
			second, err := buildForm(deps, ctx, Input{Form: Config{Request: map[string]any{"child": map[string]any{
				"Outputs":   map[string]any{"artifact_refs": map[string]any{"design.md": map[string]any{"kind": "attached", "name": "design.md"}}},
				"Artifacts": map[string]any{"design.md": ref},
			}}, ResponseSchema: map[string]any{}}})
			require.NoError(t, err)
			var bundle []byte
			publish := func(c wf.ExecutionContext, form InputForm) error {
				raw, e := json.Marshal(form)
				if e != nil {
					return e
				}
				_, e = c.DoTask(wf.TaskCall{Site: form.RequestID, TaskType: "input:collect_user_input", ContractVersion: 1, Command: raw}, func(context.Context) (jobdb.TaskData, error) {
					value, e := jsonValue(form)
					if e != nil {
						return nil, e
					}
					req := workerops.ActivityInvocationRequest{Input: value.(map[string]any)}
					req.GitTaskContext.Workspace = &contextual.WorkspaceContext{ScopeID: "review-documents"}
					td, e := workerops.NewInvocationTaskData(req)
					if e != nil {
						return nil, e
					}
					data, e := td.GetData()
					if e != nil {
						return nil, e
					}
					return &jobdb.SimpleTaskData{Data: data, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes(gitstate.ThinPackArtifactName, bundle)}}, nil
				})
				return e
			}
			f, err := changestest.Build(ctx, rt, changestest.Options{Directory: t.TempDir(), Transform: func(_ int, s *changestest.Step) { bundle = s.Bundle }, AfterStep: func(c wf.ExecutionContext, i int) error {
				if i == 1 {
					return publish(c, first)
				}
				if i == 3 {
					return publish(c, second)
				}
				return nil
			}})
			require.NoError(t, err)
			require.NoError(t, f.DeleteCheckout())
			api, err := jobinspect.NewChangesService(&jobinspect.Service{Runtime: rt, Tenant: "tenant"}, jobinspect.ChangesConfig{})
			require.NoError(t, err)
			earlier, err := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId})
			require.NoError(t, err)
			require.Equal(t, f.Commits[0], earlier.Head)
			_, err = inputs.SubmitFormResponse(ctx, "tenant", f.Job.JobId, FormSubmission{RequestID: first.RequestID, SubmissionID: "invalid", Fields: map[string]any{"decision": "invalid"}}, Actor{ID: "reviewer", Kind: "human"})
			require.Error(t, err)
			accepted, err := inputs.SubmitFormResponse(ctx, "tenant", f.Job.JobId, FormSubmission{RequestID: first.RequestID, SubmissionID: "original-approval", Fields: map[string]any{"decision": "approve"}}, Actor{ID: "reviewer", Kind: "human"})
			require.NoError(t, err)
			receipt, err := json.Marshal(accepted.Receipt)
			require.NoError(t, err)
			require.NoError(t, f.RunRecorded(ctx, rt, f.Job))
			// A fresh input runtime restores the next pending question from records.
			inputs, err = NewRuntime(ctl, nil)
			require.NoError(t, err)
			pending, err := inputs.GetForm(ctx, "tenant", f.Job.JobId)
			require.NoError(t, err)
			require.Equal(t, second.RequestID, pending.RequestID)
			inspect := func(selected jobdb.JobKey) {
				before, e := changestest.Inventory(ctx, rt, selected)
				require.NoError(t, e)
				beforeChild, e := changestest.Inventory(ctx, rt, producer.JobKey)
				require.NoError(t, e)
				latest, e := api.History(ctx, jobinspect.ChangesRequest{JobID: selected.JobId})
				require.NoError(t, e)
				require.Equal(t, f.Head, latest.Head)
				require.NotEqual(t, earlier.Selection, latest.Selection)
				history, e := NewHistoryService(HistoryConfig{Reader: ctl})
				require.NoError(t, e)
				var entries []JobInputStoryEntry
				for token := ""; ; {
					page, e := history.ListJobInputStory(ctx, ListJobInputStoryRequest{TenantID: "tenant", JobID: selected.JobId, PageSize: 1, PageToken: token})
					require.NoError(t, e)
					entries = append(entries, page.Entries...)
					if page.NextPageToken == "" {
						break
					}
					token = page.NextPageToken
				}
				require.Len(t, entries, 2)
				for i, entry := range entries {
					d, e := history.GetJobInputStoryEntry(ctx, GetJobInputStoryEntryRequest{TenantID: "tenant", JobID: selected.JobId, EntryID: entry.EntryID})
					require.NoError(t, e)
					require.Len(t, d.RequestManifest, 1, "reserved bundle must stay hidden")
					a, e := history.OpenJobInputStoryArtifact(ctx, OpenJobInputStoryArtifactRequest{TenantID: "tenant", JobID: selected.JobId, EntryID: entry.EntryID, ArtifactID: d.RequestManifest[0].ArtifactID, SnapshotToken: d.SnapshotToken})
					require.NoError(t, e)
					b, e := io.ReadAll(a)
					require.NoError(t, e)
					require.NoError(t, a.Close())
					require.Equal(t, "original child document\n", string(b))
					require.Equal(t, key, a.Ref.Stored.Key)
					if i == 0 {
						var output Output
						require.NoError(t, json.Unmarshal(d.RawAcceptedOutput, &output))
						actual, e := json.Marshal(output.Receipt)
						require.NoError(t, e)
						require.JSONEq(t, string(receipt), string(actual))
					}
				}
				after, e := changestest.Inventory(ctx, rt, selected)
				require.NoError(t, e)
				afterChild, e := changestest.Inventory(ctx, rt, producer.JobKey)
				require.NoError(t, e)
				require.Equal(t, string(before), string(after))
				require.Equal(t, string(beforeChild), string(afterChild))
			}
			inspect(f.Job)
			_, err = inputs.SubmitStructuredResponse(ctx, "tenant", f.Job.JobId, StructuredSubmission{RequestID: second.RequestID, SubmissionID: "second", Response: "continue"}, Actor{ID: "reviewer", Kind: "human"})
			require.NoError(t, err)
			require.NoError(t, f.RunRecorded(ctx, rt, f.Job))
			engine, err = wf.NewEngineBuilder().WithRuntime(rt).WithWorkerTenantId("tenant").PlusWorkers(f.Worker).BuildEngine()
			require.NoError(t, err)
			_, err = engine.ReplayJobRun(ctx, wf.ReplayRunRequest{JobKey: f.Job, JobWorker: f.Worker})
			require.NoError(t, err)
			inspect(f.Job)
			log, err := (&jobinspect.Service{Runtime: rt, Tenant: "tenant"}).Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, err)
			require.Len(t, log.Tasks, 6)
			restarted, err := rt.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{Cell: "changes", PriorJobKey: f.Job, ExecutionRestart: &jobdb.ExecutionRestart{ScopeID: log.Tasks[len(log.Tasks)-1].ID}}})
			require.NoError(t, err)
			require.NoError(t, f.RunRecorded(ctx, rt, restarted.JobKey))
			inspect(restarted.JobKey)
		})
	}
}
