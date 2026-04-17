package input

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	"github.com/stretchr/testify/require"
)

func TestBuildFormAutoFillRequiresNonZeroOutput(t *testing.T) {
	tests := []struct {
		name       string
		output     *Output
		expectAuto bool
	}{
		{name: "nil output", output: nil, expectAuto: false},
		{name: "empty response string", output: &Output{Response: ""}, expectAuto: false},
		{name: "zero numeric response", output: &Output{Response: 0}, expectAuto: false},
		{name: "non-empty response", output: &Output{Response: "value"}, expectAuto: true},
		{name: "fields present", output: &Output{Fields: map[string]interface{}{"a": "b"}}, expectAuto: true},
		{name: "metadata present", output: &Output{Metadata: map[string]interface{}{"source": "x"}}, expectAuto: true},
		{name: "user id present", output: &Output{UserID: "user-1"}, expectAuto: true},
	}

	for _, tc := range tests {
		deps := ops.NewOpDependenciesBuilder().Build()
		form, err := buildForm(deps, context.Background(), Input{
			Form: Config{
				Question: "q",
				Output:   tc.output,
			},
		})
		require.NoError(t, err, tc.name)

		typedDeps, ok := deps.(interface {
			NextTaskType() (string, bool)
		})
		require.True(t, ok, tc.name)

		nextType, set := typedDeps.NextTaskType()
		require.Equal(t, tc.expectAuto, set, tc.name)

		if tc.expectAuto {
			require.Equal(t, autoFillTaskType, nextType, tc.name)
			require.NotNil(t, form.Output, tc.name)
		} else {
			require.Nil(t, form.Output, tc.name)
		}
	}
}
