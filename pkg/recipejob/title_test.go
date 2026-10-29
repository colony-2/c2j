package recipejob_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/starter"
	workerworkflow "github.com/colony-2/c2j/pkg/worker/workflow"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func checkTitleJSON(t *testing.T, value any, title, goal string) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	if title == "" {
		require.NotContains(t, fields, "title")
	} else {
		require.Equal(t, title, fields["title"])
	}
	require.Equal(t, "unchanged prompt", fields["prompt"])
	require.Equal(t, goal, fields["goal_id"])
}

func TestSubmittedTitleMetadataAndListings(t *testing.T) {
	for _, backend := range []string{"embedded", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			var rt jobdb.WorkflowRuntime = db
			if backend == "remote" {
				server := httptest.NewServer(remote.NewServer(db))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			sdk, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
			for _, tc := range []struct {
				name  string
				input any
				want  string
			}{
				{"text", "  Improve café\nUI  ", "  Improve café\nUI  "},
				{"absent", nil, ""}, {"empty", "", ""}, {"blank", " \n ", ""},
				{"number", 42, ""}, {"object", map[string]any{"text": "not a string"}, ""},
				{"secret", secrets.Reference{Name: "private_title"}, ""},
				{"secret-template", "${{ context.secrets.private_title }}", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					start := workflowctl.StartJob{TenantId: "t", JobID: tc.name, RecipeName: "fixture", Inputs: map[string]any{"prompt": "unchanged prompt"}}
					if tc.input != nil {
						start.Inputs["title"] = tc.input
					}
					ref, err := starter.StartRecipeJob(ctx, start, engine)
					require.NoError(t, err)
					listed, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}, JobKeys: []jobdb.JobKey{ref.Key()}})
					require.NoError(t, err)
					require.Len(t, listed.Jobs, 1)
					var metadata map[string]any
					require.NoError(t, json.Unmarshal(listed.Jobs[0].Metadata, &metadata))
					if tc.want == "" {
						require.NotContains(t, metadata, "title")
					} else {
						require.Equal(t, tc.want, metadata["title"])
					}
					guard := &completionListOnly{list: rt.ListJobs}
					page, err := recipejob.ListRecipeJobs(ctx, guard, recipejob.ListRecipeJobsRequest{TenantID: "t", JobIDs: []string{ref.JobID}})
					require.NoError(t, err)
					require.Len(t, page.Jobs, 1)
					checkTitleJSON(t, page.Jobs[0], tc.want, ref.GoalID)
					checkTitleJSON(t, joblist.JobFromSummary(listed.Jobs[0]), tc.want, ref.GoalID)
					candidates, err := recipejob.ListGoalCandidates(ctx, guard, recipejob.GoalCandidatesRequest{TenantID: "t", GoalID: ref.GoalID})
					require.NoError(t, err)
					require.Len(t, candidates.Jobs, 1)
					checkTitleJSON(t, candidates.Jobs[0], tc.want, ref.GoalID)
					require.EqualValues(t, 2, guard.calls.Load())
				})
			}
		})
	}
}

func TestTitleProjectionsUseOnlySummaryMetadata(t *testing.T) {
	// Even whitespace-only titles already stored by another client are preserved.
	for _, title := range []string{"  Improve café\nUI  ", "", " \n "} {
		t.Run(title, func(t *testing.T) {
			meta, err := json.Marshal(starter.JobMetadata{Title: title, Prompt: "unchanged prompt", RecipeName: "not-installed", RepositorySource: "https://github.com/acme/app.git"})
			require.NoError(t, err)
			summary := jobdb.JobSummary{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, JobType: "recipe", GoalID: "goal", ParentJobID: "parent", Metadata: meta}
			guard := &completionListOnly{list: func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
				require.Equal(t, []string{"tenant"}, req.TenantIds)
				return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{summary}, NextPageToken: "next"}, nil
			}}
			server := httptest.NewServer(remote.NewServer(guard))
			defer server.Close()
			client, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			for _, lister := range []recipejob.Lister{guard, client} {
				ctx := context.Background()
				page, err := recipejob.ListRecipeJobs(ctx, lister, recipejob.ListRecipeJobsRequest{TenantID: "tenant", RepositorySource: "github.com/acme/app", PageSize: 1})
				require.NoError(t, err)
				require.Len(t, page.Jobs, 1)
				require.Equal(t, "next", page.NextPageToken)
				checkTitleJSON(t, page.Jobs[0], title, "goal")
				children := recipejob.ListChildRecipeJobsRequest{TenantID: "tenant", ParentTenantID: "tenant", ParentJobID: "parent", AllParentInvocations: true}
				page, err = recipejob.ListChildRecipeJobs(ctx, lister, children)
				require.NoError(t, err)
				require.Len(t, page.Jobs, 1)
				checkTitleJSON(t, page.Jobs[0], title, "goal")
				control := &workerworkflow.SWFWorkflowControl{Engine: completionListEngine{lister: lister}}
				page, err = recipejob.ListChildRecipeJobsFromWorkflow(ctx, control, children)
				require.NoError(t, err)
				require.Len(t, page.Jobs, 1)
				checkTitleJSON(t, page.Jobs[0], title, "goal")
				candidates, err := recipejob.ListGoalCandidates(ctx, lister, recipejob.GoalCandidatesRequest{TenantID: "tenant", GoalID: "goal", PageSize: 1, PageToken: "cursor"})
				require.NoError(t, err)
				require.Len(t, candidates.Jobs, 1)
				require.Equal(t, "next", candidates.NextPageToken)
				checkTitleJSON(t, candidates.Jobs[0], title, "goal")
			}
			require.EqualValues(t, 8, guard.calls.Load(), "one summary query per response; all other runtime reads are unavailable")
		})
	}
}
