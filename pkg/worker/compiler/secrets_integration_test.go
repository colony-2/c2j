package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobdbschema"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/swfutil"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	toyruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type secretTestLogBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *secretTestLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}
func (b *secretTestLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

type testSecretProvider struct {
	mu    sync.Mutex
	calls [][]string
}

func (p *testSecretProvider) ResolveSecrets(_ context.Context, req jobdb.SecretResolveRequest) (jobdb.SecretResolveResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, append([]string(nil), req.Names...))
	out := jobdb.SecretResolveResult{Secrets: map[string]jobdb.SecretValue{}}
	for _, n := range req.Names {
		if n != "missing" {
			out.Secrets[n] = jobdb.SecretValue{Value: n + "-private-value\n"}
		}
	}
	return out, nil
}
func (p *testSecretProvider) Calls() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([][]string(nil), p.calls...)
}

func TestSecretsAcrossTasksReplayAndFailures(t *testing.T) {
	// No short execution deadlines or sleeps: assert provider calls and durable
	// contents directly; expiry and concurrency use synctest in pkg/secrets.
	var logs secretTestLogBuffer
	previous := slog.Default()
	previousWriter, previousFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer func() { slog.SetDefault(previous); log.SetOutput(previousWriter); log.SetFlags(previousFlags) }()
	type input struct {
		Token string `json:"token" secret:"true"`
		Mode  string `json:"mode"`
	}
	type output struct {
		Text string `json:"text"`
	}
	op := coreops.NewActivityMappedOpV2[input, output](coreops.OpMetadata{Type: "secret_probe"}, func(_ coreops.OpDependencies, ctx context.Context, in input) (output, error) {
		if !strings.HasSuffix(in.Token, "-private-value\n") {
			return output{}, fmt.Errorf("secret not materialized")
		}
		slog.Info("probe", "value", in.Token)
		switch in.Mode {
		case "echo":
			return output{in.Token}, nil
		case "fail":
			return output{}, fmt.Errorf("probe failed: %s", in.Token)
		}
		return output{"ok"}, nil
	})
	withRegisteredOps(t, op)
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
	require.NoError(t, err)
	provider := &testSecretProvider{}
	runtime := toyruntime.New(toyruntime.WithSecretProvider(provider))
	workers := []jobworkflow.TaskWorker{}
	for _, w := range ws.TaskWorkers {
		workers = append(workers, w)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).WithWorkerTenantId("secrets").PlusWorkers(ws.JobWorker, workers...).BuildEngine()
	require.NoError(t, err)
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go engine.Run(ctx)
	for _, mode := range []string{"ok", "echo", "fail"} {
		t.Run(mode, func(t *testing.T) {
			before := len(provider.Calls())
			var source strings.Builder
			source.WriteString("id: secrets\nsequence:\n")
			for i := 0; i < 10; i++ {
				name := "token"
				if i == 9 {
					name = "future"
				}
				fmt.Fprintf(&source, "  - id: step%d\n    op: secret_probe\n    inputs:\n      token: ${{ context.secrets.%s }}\n      mode: %s\n", i, name, mode)
			}
			// An optional future branch must not make the first task fail.
			source.WriteString("  - id: branches\n    state:\n      initial: idle\n      states:\n        idle:\n          sequence: []\n        unused:\n          op: secret_probe\n          inputs:\n            token: ${{ context.secrets.missing }}\n            mode: ok\n")
			rec, err := recipe.LoadRecipeFromString([]byte(source.String()))
			require.NoError(t, err)
			job, git := GenerateTestContext()
			key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "secrets", RecipeName: "secrets", JobContext: job, GitRef: git.ParentRef}, engine, *rec)
			require.NoError(t, err)
			require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 30*time.Second, key, engine))
			_, err = swfutil.JobResult(ctx, engine, key)
			if mode == "ok" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "private-value")
			}
			after := len(provider.Calls())
			if mode == "ok" {
				require.Equal(t, before+1, after)
			}
			require.Greater(t, after, before)
			require.ElementsMatch(t, []string{"token", "future", "missing"}, provider.Calls()[before])
			run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
			require.NoError(t, err)
			raw, err := json.Marshal(run)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-value")
			require.Contains(t, string(raw), "$c2j_secret")
			require.Nil(t, registry.SecretSessions.Get(key))
			_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
			if mode == "ok" {
				require.NoError(t, err)
			}
			require.Len(t, provider.Calls(), after)
		})
	}
	require.NotContains(t, logs.String(), "private-value")
	require.Contains(t, logs.String(), "[REDACTED]")
}

func TestSecretFallbacksInLiveTasksAndReplay(t *testing.T) {
	type input struct {
		Token    string `json:"token" secret:"true"`
		Expected string `json:"expected"`
	}
	var calls atomic.Int32
	op := coreops.NewActivityMappedOpV2[input, map[string]any](coreops.OpMetadata{Type: "fallback_probe"}, func(_ coreops.OpDependencies, _ context.Context, in input) (map[string]any, error) {
		calls.Add(1)
		if in.Token != in.Expected {
			return nil, fmt.Errorf("unexpected materialized input")
		}
		return map[string]any{"ok": true}, nil
	})
	withRegisteredOps(t, op)
	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry)
	require.NoError(t, err)
	provider := &testSecretProvider{}
	runtime := toyruntime.New(toyruntime.WithSecretProvider(provider))
	workers := []jobworkflow.TaskWorker{}
	for _, w := range ws.TaskWorkers {
		workers = append(workers, w)
	}
	engine, err := jobworkflow.NewEngineBuilder().WithRuntime(runtime).WithWorkerTenantId("fallbacks").PlusWorkers(ws.JobWorker, workers...).BuildEngine()
	require.NoError(t, err)
	engine = jobdbschema.WorkflowEngine{Engine: engine, Registry: runtime}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go engine.Run(ctx)
	source := `id: fallbacks
input_schema:
  token: {type: string, secret: true, default_value: "schema-default"}
  omitted: {type: string, secret: true, default_value: ""}
inputs:
  token: '${{ inputs.token }}'
  omitted: '${{ inputs.omitted }}'
sequence:
  - id: explicit
    op: fallback_probe
    inputs:
      token: '${{ context.secrets.?missing.orValue("first") }}'
      expected: first
  - id: another
    op: fallback_probe
    inputs:
      token: '${{ context.secrets.?missing.orValue("second") }}'
      expected: second
  - id: schema
    op: fallback_probe
    inputs:
      token: '${{ inputs.token }}'
      expected: schema-default
  - id: omitted
    op: fallback_probe
    inputs:
      token: '${{ inputs.omitted }}'
      expected: ""
`
	rec, err := recipe.LoadRecipeFromString([]byte(source))
	require.NoError(t, err)
	job, git := GenerateTestContext()
	key, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{
		TenantId: "fallbacks", RecipeName: "fallbacks", JobContext: job, GitRef: git.ParentRef,
		Inputs: map[string]any{"token": "${{ context.secrets.missing }}"},
	}, engine, *rec)
	require.NoError(t, err)
	require.NoError(t, jobworkflow.WaitForJobToComplete(ctx, 30*time.Second, key, engine))
	_, err = swfutil.JobResult(ctx, engine, key)
	require.NoError(t, err)
	require.EqualValues(t, 4, calls.Load())
	require.Len(t, provider.Calls(), 3, "missing values and defaults must not be cached; omitted default needs no fetch")
	run, err := engine.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true})
	require.NoError(t, err)
	raw, err := json.Marshal(run)
	require.NoError(t, err)
	require.Contains(t, string(raw), "$c2j_secret")
	require.NotContains(t, string(raw), "private-value")
	_, err = engine.ReplayJobRun(ctx, jobworkflow.ReplayRunRequest{JobKey: key, JobWorker: NewRecipeJobWorker(RecipeJobWorkerOptions{ReadOnlyReplay: true})})
	require.NoError(t, err)
	require.EqualValues(t, 4, calls.Load())
	require.Len(t, provider.Calls(), 3)
}
