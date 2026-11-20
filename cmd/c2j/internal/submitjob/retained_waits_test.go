package submitjob

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestSelectedPreparationRejectsLostConstraints(t *testing.T) {
	instant := time.Date(2027, 1, 1, 12, 20, 0, 123456789, time.UTC)
	deps := []jobdb.Dependency{{Target: jobdb.DependencyTarget{GoalID: "goal"}, Condition: jobdb.JobPrereqSuccess}}
	for _, change := range []string{"none", "outer-time", "outer-dependencies", "input-time", "input-dependencies"} {
		t.Run(change, func(t *testing.T) {
			input := workflowctl.StartJob{AvailableAt: &instant, Dependencies: deps}
			d := starter.Descriptor{Job: jobdb.SubmitJob{AvailableAt: &instant, Dependencies: deps}}
			switch change {
			case "outer-time":
				d.Job.AvailableAt = nil
			case "outer-dependencies":
				d.Job.Dependencies = nil
			case "input-time":
				input.AvailableAt = nil
			case "input-dependencies":
				input.Dependencies = nil
			}
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			d.Input = raw
			err = validatePreparedWaits(d, deps, &instant)
			if change == "none" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "changed dependencies or earliest start")
			}
		})
	}
}
