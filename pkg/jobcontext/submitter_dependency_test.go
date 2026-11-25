package jobcontext

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubmitterDependencyMerge(t *testing.T) {
	value := false
	unmarked := StartedJobContext{TenantID: "t", JobID: "a", RecipeName: "one", Cell: "app", CellID: "cell", CellRepo: "example.test/app"}
	marked := unmarked
	marked.SubmitterDependency = &value
	sibling := unmarked
	sibling.JobID = "b"
	for _, pair := range [][2]StartedJobContext{{unmarked, marked}, {marked, unmarked}} {
		out, err := MergeStartedJobs(StartedJobsContext{Items: []StartedJobContext{pair[0], sibling}}, StartedJobsContext{Items: []StartedJobContext{pair[1]}})
		require.NoError(t, err)
		require.Equal(t, []StartedJobContext{marked, sibling}, out.Items)
		require.Equal(t, []string{"a", "b"}, out.JobIDs)
		require.Empty(t, out.GoalIDs)
	}
}
