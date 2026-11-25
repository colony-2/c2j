package executionruntime

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestExtensionEAGAINWakeMustMatch(t *testing.T) {
	route := jobdb.Route{JobType: "recipe"}
	task := wf.TaskContext{InvocationID: "wait", AttemptNumber: 1}
	active := resourceretry.WithSupport(context.Background(), true)
	for _, name := range []string{"matching", "unrelated", "wrong-attempt", "wrong-route", "pending-task", "cancelled", "unsupported"} {
		t.Run(name, func(t *testing.T) {
			state := jobdb.ExecutionState{ResourceResume: &jobdb.ResourceResume{Version: 1, Invocation: "wait/attempt:1", Attempt: 1, Route: route}}
			local, cancel := context.WithCancel(active)
			defer cancel()
			switch name {
			case "unrelated":
				state.ResourceResume.Invocation = "another/attempt:1"
			case "wrong-attempt":
				state.ResourceResume.Attempt = 2
			case "wrong-route":
				state.ResourceResume.Route = jobdb.Route{JobType: "other"}
			case "pending-task":
				state.TaskWait = &jobdb.TaskWait{ResumeJobType: route.JobType}
			case "cancelled":
				cancel()
			case "unsupported":
				local = resourceretry.WithSupport(local, false)
			}
			err := WaitForContinuation(context.WithValue(local, continuationLeaseKey{}, continuationLease{route, state}), task)
			if name == "matching" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
