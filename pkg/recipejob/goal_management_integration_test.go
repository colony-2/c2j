package recipejob_test

import (
	"context"
	"errors"
	"github.com/colony-2/c2j/pkg/recipejob"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestExactJobGoalMutationUnsupported(t *testing.T) {
	_, err := recipejob.FailGoal(context.Background(), nil, jobdb.FailGoalRequest{GoalKey: jobdb.GoalKey{TenantId: "t", GoalID: "G"}, Message: "stop"})
	require.ErrorIs(t, err, errors.ErrUnsupported)
}
