package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/pkg/git/gitstate"
	recipeops "github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
)

func TestTaskWorkerRunReturnsTaskDataOnFailure(t *testing.T) {
	t.Parallel()

	baseRepo, baseHash, cleanup := setupGitRepo(t)
	defer cleanup()

	reg := ActivityRegistration{
		TaskType: "test.failure",
		Step: recipeops.TaskStep{
			Invoke: func(deps recipeops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
				require.NoError(t, deps.AddOutputArtifact(&mockArtifact{name: "stderr.txt", data: []byte("boom")}))
				target := filepath.Join(deps.WorktreePath(), "mutated.txt")
				require.NoError(t, os.WriteFile(target, []byte("changed"), 0o644))
				return map[string]interface{}{"status": "failed", "detail": "boom"}, fmt.Errorf("step failed")
			},
		},
	}

	worker := &taskWorker{
		name: "test.failure",
		reg:  reg,
		doer: &opExecutor{
			deps:       recipeops.NewServiceDepsBuilder().Build(),
			reg:        reg,
			controller: gitstate.NewController(nil),
		},
	}

	input := swf.NewTaskDataOrPanic(ActivityInvocationRequest{
		Input: map[string]interface{}{},
		GitTaskContext: gitstate.GlobalGitTaskContext{
			BaseRepo: baseRepo,
			BaseRef:  baseHash,
			CellPath: "cells/test",
		},
	})

	td, err := worker.Run(swf.NewTaskContext(swf.JobKey{TenantId: "tenant", JobId: "job"}, 1, nil, nil, nil), input)
	require.Error(t, err)
	require.NotNil(t, td)

	artifacts, err := td.GetArtifacts()
	require.NoError(t, err)
	require.NotEmpty(t, artifacts)

	raw, err := td.GetData()
	require.NoError(t, err)

	var env coretask.OutputEnvelope
	require.NoError(t, json.Unmarshal(raw, &env))
	require.Equal(t, coretask.OutputKindActivityInvocationOutput, env.Kind)

	var output ActivityInvocationOutput
	require.NoError(t, env.DecodePayload(&output))
	require.Equal(t, "failed", output.OpOutput["status"])
	require.Equal(t, "boom", output.OpOutput["detail"])
	require.Equal(t, "stderr.txt", artifacts[0].Name())
}
