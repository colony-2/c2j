package funcregistry

import (
	"context"
	"testing"

	"github.com/colony-2/c2j/pkg/core/contextual"
	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
)

func TestAddZeroFuncRegistersAndExecutes(t *testing.T) {
	builder := NewBuilder()
	AddZeroFunc(builder, "cells", func(ctx context.Context) ([]map[string]interface{}, error) {
		return []map[string]interface{}{
			{"name": "a", "id": "1", "path": "/a"},
			{"name": "b", "id": "2", "path": "/b"},
		}, nil
	})

	env, err := cel.NewEnv(builder.TypeOptions()...)
	require.NoError(t, err)
	opts, err := builder.FunctionOptions(env.CELTypeAdapter())
	require.NoError(t, err)
	env, err = env.Extend(opts...)
	require.NoError(t, err)

	ast, iss := env.Compile(`cells()[0].name == "a" && cells()[1].path == "/b"`)
	require.Nil(t, iss.Err())

	prg, err := env.Program(ast)
	require.NoError(t, err)
	out, _, err := prg.Eval(map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, true, out.Value())
}

func TestAddZeroFuncWithContextReceivesTaskContext(t *testing.T) {
	builder := NewBuilder()
	AddZeroFuncWithContext(builder, "ctx_project", func(ctx context.Context, task contextual.TaskExecutionContext) ([]CELCell, error) {
		return []CELCell{{
			Name: "cell",
			ID:   task.Workflow.ProjectId + "-id",
			Path: "/p",
		}}, nil
	})

	env, err := cel.NewEnv(builder.TypeOptions()...)
	require.NoError(t, err)

	ctxProvider := func() contextual.TaskExecutionContext {
		return contextual.TaskExecutionContext{
			Workflow: contextual.WorkflowContext{
				ProjectId: "proj-123",
			},
		}
	}

	opts, err := builder.FunctionOptionsWithContext(env.CELTypeAdapter(), ctxProvider)
	require.NoError(t, err)
	env, err = env.Extend(opts...)
	require.NoError(t, err)

	ast, iss := env.Compile(`ctx_project()[0].id`)
	require.Nil(t, iss.Err())

	prg, err := env.Program(ast)
	require.NoError(t, err)
	out, _, err := prg.Eval(map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, "proj-123-id", out.Value())
}

func TestTemplateFuncsWithContext(t *testing.T) {
	builder := NewBuilder().WithDefaults()
	AddZeroFuncWithContext(builder, "cells", func(ctx context.Context, task contextual.TaskExecutionContext) ([]CELCell, error) {
		return []CELCell{{
			Name: "cell-a",
			ID:   task.Workflow.ProjectId,
			Path: "/a",
		}}, nil
	})

	funcs := builder.TemplateFuncsWithContext(func() contextual.TaskExecutionContext {
		return contextual.TaskExecutionContext{
			Workflow: contextual.WorkflowContext{ProjectId: "proj-1"},
		}
	})

	cellsFn, ok := funcs["cells"].(func() (any, error))
	require.True(t, ok)
	cellsOut, err := cellsFn()
	require.NoError(t, err)
	cellsList, ok := cellsOut.([]interface{})
	require.True(t, ok)
	require.Len(t, cellsList, 1)
	first, ok := cellsList[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "proj-1", first["id"])

	jqFn, ok := funcs["jq"].(func(any, string) (any, error))
	require.True(t, ok)
	jqOut, err := jqFn(map[string]any{"user": map[string]any{"name": "Ada"}}, ".user.name")
	require.NoError(t, err)
	require.Equal(t, "Ada", jqOut)
}
