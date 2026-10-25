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
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestSubmittedPromptMetadataAndListings(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
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
			sdk, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
			require.NoError(t, err)
			engine := jobdbschema.WorkflowEngine{Engine: sdk, Registry: rt.(jobdb.JobSchemaRegistry)}
			for _, tc := range []struct {
				name  string
				input any
				want  string
			}{
				{"text", "  Improve café\nKeep the API stable.  ", "  Improve café\nKeep the API stable.  "},
				{"absent", nil, ""}, {"empty", "", ""}, {"blank", " \n ", ""}, {"object", map[string]any{"text": "not a string"}, ""},
				{"secret", secrets.Reference{Name: "private_prompt"}, ""},
				{"secret-template", "${{ context.secrets.private_prompt }}", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					start := workflowctl.StartJob{TenantId: "t", JobID: tc.name, RecipeName: "fixture"}
					if tc.input != nil {
						start.Inputs = map[string]any{"prompt": tc.input}
					}
					ref, err := starter.StartRecipeJob(ctx, start, engine)
					require.NoError(t, err)
					listed, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}, JobKeys: []jobdb.JobKey{ref.Key()}})
					require.NoError(t, err)
					require.Len(t, listed.Jobs, 1)
					var metadata map[string]any
					require.NoError(t, json.Unmarshal(listed.Jobs[0].Metadata, &metadata))
					if tc.want == "" {
						require.NotContains(t, metadata, "prompt")
					} else {
						require.Equal(t, tc.want, metadata["prompt"])
					}
					general := joblist.JobFromSummary(listed.Jobs[0])
					require.Equal(t, tc.want, general.Prompt)
					recipe, err := recipejob.GetRecipeJob(ctx, rt, recipejob.GetRecipeJobRequest{TenantID: "t", JobID: ref.JobID})
					require.NoError(t, err)
					require.Equal(t, tc.want, recipe.Prompt)
					for _, view := range []any{general, recipe} {
						raw, err := json.Marshal(view)
						require.NoError(t, err)
						var fields map[string]any
						require.NoError(t, json.Unmarshal(raw, &fields))
						if tc.want == "" {
							require.NotContains(t, fields, "prompt")
						} else {
							require.Equal(t, tc.want, fields["prompt"])
						}
					}
				})
			}
		})
	}
}
