package ops

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type dependencyJobList []workflowctl.JobItem

func (l dependencyJobList) ListJobs(context.Context, jobdb.ListJobsRequest) ([]workflowctl.JobItem, string, error) {
	return l, "", nil
}

func TestSubmitterDependencyCollection(t *testing.T) {
	for _, tc := range []struct{ name, property, want string }{
		{"true", `,"submitter_dependency":true`, "true"},
		{"false", `,"submitter_dependency":false`, "false"},
		{"omitted", "", ""},
		{"null", `,"submitter_dependency":null`, ""},
		{"string", `,"submitter_dependency":"false"`, ""},
		{"numeric", `,"submitter_dependency":1`, ""},
		{"object", `,"submitter_dependency":{}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lister := dependencyJobList{{JobSummary: jobdb.JobSummary{
				Cell: "child-cell", CellID: "cell-id", CellRepo: "repo", JobKey: jobdb.JobKey{TenantId: "t", JobId: "child"}, GoalID: "goal", Status: jobdb.JobStatusReady,
				Metadata: json.RawMessage(`{"recipe":"child_recipe"` + tc.property + `}`),
			}}}
			jobs, err := collectStartedJobs(context.Background(), lister, jobcontext.Current{TenantID: "t", JobID: "parent", InvocationHash: "launch"})
			require.NoError(t, err)
			require.Equal(t, []string{"child"}, jobs.JobIDs)
			require.Empty(t, jobs.GoalIDs)
			require.Len(t, jobs.Items, 1)
			raw, err := json.Marshal(jobs.Items[0])
			require.NoError(t, err)
			var item map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &item))
			require.Equal(t, tc.want, string(item["submitter_dependency"]))
			jobs.Items[0].SubmitterDependency = nil
			require.Equal(t, jobcontext.StartedJobContext{Cell: "child-cell", CellID: "cell-id", CellRepo: "repo", TenantID: "t", JobID: "child", RecipeName: "child_recipe", Status: "READY", ParentInvocationHash: "launch"}, jobs.Items[0])
		})
	}
}
