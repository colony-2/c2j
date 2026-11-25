package submitjob

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubmitterDependencyOptions(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		set         bool
		change      func(*Options)
		wantError   string
	}{
		{name: "omitted"},
		{name: "true", value: "true", set: true},
		{name: "false", value: "false", set: true},
		{name: "empty", set: true, wantError: "requires true or false"},
		{name: "numeric", value: "1", set: true, wantError: "requires true or false"},
		{name: "case", value: "FALSE", set: true, wantError: "requires true or false"},
		{name: "standalone", value: "false", set: true, change: func(o *Options) { o.route = nil; o.SWFURL = "embed:///" }, wantError: "requires a live task"},
		{name: "goal", value: "false", set: true, change: func(o *Options) { o.GoalID = "goal" }, wantError: "is not supported with"},
		{name: "retained", value: "false", set: true, change: func(o *Options) { o.RequestFile = "request.json" }, wantError: "is not supported with"},
		{name: "replay", value: "false", set: true, change: func(o *Options) { o.ReplayFile = "request.json" }, wantError: "is not supported with"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{TenantID: "test", route: &submissionRoute{hasBroker: true}, SubmitterDependency: tc.value, SubmitterDependencySet: tc.set}
			if tc.change != nil {
				tc.change(&opts)
			}
			err := opts.Validate()
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}
