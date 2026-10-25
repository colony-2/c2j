package runjob

import (
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestBlockingDiagnosticsPreserveTypedRoute(t *testing.T) {
	route := &jobdb.Route{JobType: " recipe:Kind ", TaskType: "input:collect:Step"}
	outcome := jobworkflow.JobRunOutcome{NextRoute: route, MissingRoute: route}
	require.True(t, shouldFailNotReady("fail-on-missing-capability", outcome))
	require.False(t, shouldFailNotReady("fail-on-missing-capability", jobworkflow.JobRunOutcome{}))
	detail := describeBlocking(outcome)
	require.Contains(t, detail, `missing_route={"jobType":" recipe:Kind ","taskType":"input:collect:Step"}`)
	require.Contains(t, detail, `next_route={"jobType":" recipe:Kind ","taskType":"input:collect:Step"}`)
}

func TestBlockingDiagnosticsIncludeGoalAndExactJobTargets(t *testing.T) {
	outcome := jobworkflow.JobRunOutcome{BlockingWait: &jobdb.DependencyWaitInfo{WaitID: "wait-1", State: jobdb.DependencyPending, Dependencies: []jobdb.DependencyResolution{
		{Target: jobdb.DependencyTarget{GoalID: "goal-1"}, State: jobdb.DependencyPending},
		{Target: jobdb.DependencyTarget{JobID: "job-2"}, State: jobdb.DependencyPending},
	}}}
	detail := describeBlocking(outcome)
	require.Contains(t, detail, "wait_id=wait-1")
	require.Contains(t, detail, "goal=goal-1")
	require.Contains(t, detail, "job=job-2")
}
