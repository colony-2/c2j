// Package parentagetest checks stored submission relationships, independently
// of the parent attribution serialized in recipe input and metadata.
package parentagetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type Runtime interface {
	jobinspect.Runtime
}

func Check(t *testing.T, ctx context.Context, rt Runtime, parent jobdb.JobKey, children ...string) {
	t.Helper()
	for _, child := range children {
		rows, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{parent.TenantId}, JobKeys: []jobdb.JobKey{{TenantId: parent.TenantId, JobId: child}}, IncludeHidden: true, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}})
		require.NoError(t, err)
		require.Len(t, rows.Jobs, 1)
		require.Equal(t, parent.JobId, rows.Jobs[0].ParentJobID, "stored parent of %s", child)
	}
	found := []string{}
	cursor := ""
	for {
		page, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{parent.TenantId}, ParentJobIDs: []string{parent.JobId}, IncludeHidden: true, Stores: []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}, PageSize: 1, PageToken: cursor})
		require.NoError(t, err)
		for _, row := range page.Jobs {
			found = append(found, row.JobKey.JobId)
			require.Equal(t, parent.JobId, row.ParentJobID)
		}
		if page.NextPageToken == "" {
			break
		}
		require.NotEqual(t, cursor, page.NextPageToken)
		cursor = page.NextPageToken
	}
	require.ElementsMatch(t, children, found, "formal child query, all pages and statuses")
	dag, err := jobinspect.ReadDAG(ctx, &jobinspect.Service{Runtime: rt, Tenant: parent.TenantId}, parent.JobId, jobinspect.DefaultDAGOptions())
	require.NoError(t, err)
	raw, err := json.Marshal(dag)
	require.NoError(t, err)
	var wire jobinspect.DAG
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.True(t, wire.Coverage.Complete, "%s", raw)
	for _, child := range children {
		require.Contains(t, wire.Edges, jobinspect.SubmissionEdge{Type: "submission", Parent: parent.JobId, Child: child})
	}
	t.Logf("stored parent=%s; discovered children=%v; DAG coverage complete", parent.JobId, found)
}
