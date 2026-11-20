package submitjob

import (
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestWaitFlagsPreserveLiteralOrder(t *testing.T) {
	var opts Options
	flags := pflag.NewFlagSet("waits", pflag.ContinueOnError)
	opts.AddWaitFlags(flags)
	require.NoError(t, flags.Parse([]string{"--wait-goal", " same ", "--wait-job", "a,b", "--wait-job-success", "same", "--wait-job", "a,b", "--wait-until", "1h"}))
	deps, err := opts.dependencies()
	require.NoError(t, err)
	require.Equal(t, []jobdb.Dependency{
		{Target: jobdb.DependencyTarget{GoalID: "same"}, Condition: jobdb.JobPrereqSuccess},
		{Target: jobdb.DependencyTarget{JobID: "a,b"}, Condition: jobdb.JobPrereqComplete},
		{Target: jobdb.DependencyTarget{JobID: "same"}, Condition: jobdb.JobPrereqSuccess},
	}, deps)
	require.Equal(t, []string{"1h"}, opts.WaitUntil)
}

func TestWaitValidation(t *testing.T) {
	for _, waits := range [][]Wait{
		{{WaitJob, "x"}, {WaitJobSuccess, "x"}},
		{{WaitJobSuccess, "x"}, {WaitJob, "x"}},
		{{WaitJob, " "}}, {{WaitJobSuccess, ""}}, {{WaitGoal, "\t"}},
		{{WaitJob, "self"}}, {{WaitJobSuccess, " self "}}, {{"unknown", "x"}},
	} {
		opts := Options{Waits: waits, JobID: "self", TenantID: "t", SWFURL: "http://test"}
		require.Error(t, opts.Validate(), "%v", waits)
	}
	for _, args := range [][]string{
		{"--wait-job="}, {"--wait-job-success="}, {"--wait-goal="}, {"--wait-until="},
		{"--wait-until=1h", "--wait-until=1h"}, {"--wait-until=", "--wait-until="},
	} {
		opts := Options{TenantID: "t", SWFURL: "http://test"}
		flags := pflag.NewFlagSet("waits", pflag.ContinueOnError)
		opts.AddWaitFlags(flags)
		require.NoError(t, flags.Parse(args))
		opts.ReplayFile = "saved"
		require.ErrorContains(t, opts.Validate(), "--replay")
		opts.ReplayFile = ""
		if len(opts.WaitUntil) != 1 {
			require.Error(t, opts.Validate())
		}
	}
}
