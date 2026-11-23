package recipejob_test

import (
	"context"
	"errors"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExactJobDiscoveryUnsupported(t *testing.T) {
	_, err := recipejob.ListGoals(context.Background(), nil, jobdb.ListGoalsRequest{TenantId: "t"})
	require.ErrorIs(t, err, errors.ErrUnsupported)
}
