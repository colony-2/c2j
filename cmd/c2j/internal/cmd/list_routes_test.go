package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListRouteFlagsDoNotSplitIdentifiersOrJSON(t *testing.T) {
	cmd := newListCmd()
	route := `{"jobType":"recipe:kind,one","taskType":"input:collect,two"}`
	require.NoError(t, cmd.Flags().Parse([]string{
		"--waiting-for", route, "--job-type", "recipe:kind,one", "--job-type", " Other ",
	}))
	routes, err := cmd.Flags().GetStringArray("waiting-for")
	require.NoError(t, err)
	require.Equal(t, []string{route}, routes)
	jobTypes, err := cmd.Flags().GetStringArray("job-type")
	require.NoError(t, err)
	require.Equal(t, []string{"recipe:kind,one", " Other "}, jobTypes)
}
