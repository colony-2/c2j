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
