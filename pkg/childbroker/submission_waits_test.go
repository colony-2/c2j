package childbroker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestBrokerHTTPPreservesSubmissionWaits(t *testing.T) {
	deps := []jobdb.Dependency{
		{Target: jobdb.DependencyTarget{JobID: "literal,a"}, Condition: jobdb.JobPrereqComplete},
		{Target: jobdb.DependencyTarget{JobID: "success"}, Condition: jobdb.JobPrereqSuccess},
		{Target: jobdb.DependencyTarget{GoalID: "goal"}, Condition: jobdb.JobPrereqSuccess},
	}
	instant := time.Date(2027, 1, 1, 12, 20, 0, 123456789, time.UTC)
	for _, tc := range []struct {
		name      string
		deps      []jobdb.Dependency
		available *time.Time
	}{
		{"completion", deps[:1], nil}, {"success", deps[1:2], nil}, {"goal", deps[2:], nil},
		{"time", nil, &instant}, {"mixed", deps, &instant},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			capture := &captureSubmitter{}
			server, err := Start(ctx, Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "parent", GoalID: "parent-goal"}, Submitter: capture})
			require.NoError(t, err)
			defer server.Close()
			start := workflowctl.StartJob{TenantId: "tenant", JobID: "child", Cell: "test", CellRepo: "example.test/test", RecipeName: "build", Dependencies: tc.deps, AvailableAt: tc.available}
			req, err := NewSubmitRequest(ctx, start, nil)
			require.NoError(t, err)
			_, err = Submit(ctx, brokerConfig(t, server), req)
			require.NoError(t, err)
			require.Equal(t, tc.deps, capture.last.Dependencies)
			require.Equal(t, tc.available, capture.last.AvailableAt)
			raw, err := capture.last.Data.GetData()
			require.NoError(t, err)
			var input workflowctl.StartJob
			require.NoError(t, json.Unmarshal(raw, &input))
			require.Equal(t, tc.deps, input.Dependencies)
			require.Equal(t, tc.available, input.AvailableAt)
		})
	}
}
