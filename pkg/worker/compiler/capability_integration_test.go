package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/core/contextual"
	coreops "github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/core/recipe"
	"github.com/colony-2/c2j/pkg/core/starter"
	"github.com/colony-2/c2j/pkg/core/swfutil"
	coretask "github.com/colony-2/c2j/pkg/core/task"
	"github.com/colony-2/c2j/pkg/core/workflowctl"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/stretchr/testify/require"
)

type firstStepOutput struct {
	First bool `json:"first"`
}

type secondStepOutput struct {
	Second bool `json:"second"`
}

// Integration: step1 runs as a task, step2 is disallowed and must be claimed via FindTasksWaitingForCapability.
func TestMultiStepWithCapabilityClaim(t *testing.T) {
	originalOps := coreops.List()
	t.Cleanup(func() {
		coreops.Clear()
		if len(originalOps) > 0 {
			coreops.Register(originalOps...)
		}
	})
	coreops.Clear()

	// Two-step op: first allowed, second disallowed as a task.
	opType := "two-step-op"
	builder := coreops.NewOp().
		WithType(opType).
		AddStep(opType, coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, in map[string]interface{}) (firstStepOutput, error) {
			return firstStepOutput{First: true}, nil
		})).
		AddStep("second", coreops.StepDisallow(coreops.NewStepWithDeps(func(_ coreops.OpDependencies, _ context.Context, in firstStepOutput) (secondStepOutput, error) {
			return secondStepOutput{Second: true}, nil
		})))
	op, err := builder.Build()
	require.NoError(t, err)
	coreOp := op.(coreops.RegisterableOp)
	chain := coreOp.TaskChain()
	require.Len(t, chain, 2)
	require.Equal(t, fmt.Sprintf("%s:second", opType), chain[0].NextStepTask)
	coreops.Register(coreOp)

	registry, err := workerops.NewActivityRegistry()
	require.NoError(t, err)
	ws, err := NewRecipeWorker(coreops.NewServiceDepsBuilder().Build(), registry, nil)
	require.NoError(t, err)
	var (
		jobRunsMu sync.Mutex
		jobRuns   int
		jobErr    error
	)
	jobWorkerType := fmt.Sprintf("%T", ws.JobWorker)
	ws.JobWorker = loggingJobWorker{
		inner: ws.JobWorker,
		after: func(err error) {
			jobRunsMu.Lock()
			defer jobRunsMu.Unlock()
			jobRuns++
			jobErr = err
		},
	}
	require.NotContains(t, (*ws).TaskWorkers, fmt.Sprintf("%s:second", opType))
	firstTaskType := fmt.Sprintf("%s:%s", opType, opType)
	firstReg, ok := registry.Get(firstTaskType)
	require.True(t, ok, "first step registration missing")
	require.Equal(t, fmt.Sprintf("%s:second", opType), firstReg.NextTaskType)
	var (
		firstEnvelopeMu  sync.Mutex
		firstEnvelopeSet bool
		firstEnvelope    workerops.ActivityInvocationOutput
		workerNextTask   string
		workerDebugInfo  workerDebug
		workerRunCount   int
	)
	origWorker := (*ws).TaskWorkers[firstTaskType]
	(*ws).TaskWorkers[firstTaskType] = loggingTaskWorker{
		inner: origWorker,
		capture: func(env workerops.ActivityInvocationOutput, dbg workerDebug) {
			firstEnvelopeMu.Lock()
			defer firstEnvelopeMu.Unlock()
			if !firstEnvelopeSet {
				firstEnvelope = env
				firstEnvelopeSet = true
			}
			workerNextTask = dbg.next
			workerDebugInfo = dbg
			workerRunCount++
		},
	}

	// Ensure second step is registered but disallowed as task.
	foundSecond := false
	for taskType, reg := range registry.GetAll() {
		if strings.HasSuffix(taskType, ":second") {
			foundSecond = true
			require.True(t, reg.Step.DisallowAsTask, "second step should be disallowed as task")
		}
	}
	require.True(t, foundSecond, "expected second step registration")

	// Toy engine with our workset.
	engine := newToyEngineWithWorkSet(t, ws, nil)

	repoPath, baseHash := makeTwoCommitRepo(t)
	require.NoError(t, err)

	// Recipe that invokes the two-step op.
	rec := recipe.Recipe{
		RecipeImpl: &recipe.RecipeOp{
			RecipeMetadata: recipe.RecipeMetadata{
				NodeMetadata: recipe.NodeMetadata{
					ID:     "capability-test",
					Inputs: map[string]interface{}{},
				},
			},
			OpData: recipe.OpData{Op: opType},
		},
	}

	start := workflowctl.StartJob{
		TenantId:   "test-tenant",
		RecipeName: rec.GetMetadata().ID,
		Inputs:     map[string]interface{}{},
		JobContext: contextual.JobContext{
			Actor: contextual.ActorContext{
				TicketID:   "T-123",
				ActorName:  "tester",
				ActorEmail: "tester@example.com",
			},
			Environment: contextual.EnvironmentContext{},
			Workflow: contextual.WorkflowContext{
				CellName: "cells/test",
				CellPath: "cells/test",
			},
			GitBase: contextual.GitBaseContext{
				BaseRepo:         repoPath,
				BaseRef:          baseHash,
				ResolvedBaseHash: baseHash,
			},
		},
		GitRef: baseHash,
	}

	type jobResult struct {
		key swf.JobKey
		err error
	}
	jobCh := make(chan jobResult, 1)
	go func() {
		key, err := starter.StartRecipeJob(context.Background(), start, engine, rec)
		jobCh <- jobResult{key: key, err: err}
	}()

	// Wait for the second step to become pending (disallowed as task).
	var handles []swf.TaskHandle
	for i := 0; i < 20; i++ {
		handles, err = engine.FindTasksWaitingForCapability(context.Background(), starter.RecipeJobType, opType+":second", []string{"test-tenant"})
		require.NoError(t, err)
		if len(handles) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(handles) == 0 {
		var status swf.JobStatus
		var outputs map[string]interface{}
		var env workerops.ActivityInvocationOutput
		var regNext string
		var dbg workerDebug
		var captured bool
		var runs int
		var rawResult string
		var jobRunsLocal int
		var jobErrLocal error
		jobWorkerTypeLocal := jobWorkerType
		select {
		case jr := <-jobCh:
			require.NoError(t, jr.err)
			status, _ = swfutil.JobStatus(context.Background(), engine, jr.key)
			if result, err := swfutil.JobResult(context.Background(), engine, jr.key); err == nil {
				if raw, err := result.GetData(); err == nil {
					rawResult = string(raw)
					_ = json.Unmarshal(raw, &outputs)
				}
			}
			firstEnvelopeMu.Lock()
			env = firstEnvelope
			regNext = workerNextTask
			dbg = workerDebugInfo
			captured = firstEnvelopeSet
			runs = workerRunCount
			firstEnvelopeMu.Unlock()
			jobRunsMu.Lock()
			jobRunsLocal = jobRuns
			jobErrLocal = jobErr
			jobRunsMu.Unlock()
		default:
		}
		t.Fatalf("expected pending second step task; status=%s outputs=%v rawResult=%q captured=%t runs=%d firstNextTask=%q workerNextTask=%q workerDebug=%+v jobRuns=%d jobErr=%v jobWorkerType=%s", status, outputs, rawResult, captured, runs, env.NextTask, regNext, dbg, jobRunsLocal, jobErrLocal, jobWorkerTypeLocal)
	}
	jobKey := handles[0].JobKey()

	// Decode invocation payload for git context.
	data, err := handles[0].Data()
	require.NoError(t, err)
	raw, err := data.GetData()
	require.NoError(t, err)
	var req workerops.ActivityInvocationRequest
	require.NoError(t, json.Unmarshal(raw, &req))

	envelope := workerops.ActivityInvocationOutput{
		OpOutput: map[string]interface{}{"second": true},
		GitResult: contextual.GitCommitContext{
			ParentHash:  req.GitTaskContext.ParentHash,
			PersistHash: req.GitTaskContext.PersistHash,
		},
	}
	outEnv, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, envelope)
	require.NoError(t, err)
	err = handles[0].Finish(context.Background(), swf.NewTaskDataOrPanic(outEnv))
	require.NoError(t, err)

	require.NoError(t, swf.WaitForJobToComplete(context.Background(), 5*time.Second, jobKey, engine))
	jobRes := <-jobCh
	require.NoError(t, jobRes.err)
	require.Equal(t, jobKey, jobRes.key)
	firstEnvelopeMu.Lock()
	jobRunsMu.Lock()
	t.Logf("post-completion: captured=%t runs=%d env=%+v workerDbg=%+v jobRuns=%d jobErr=%v", firstEnvelopeSet, workerRunCount, firstEnvelope, workerDebugInfo, jobRuns, jobErr)
	jobRunsMu.Unlock()
	firstEnvelopeMu.Unlock()
	resultData, err := swfutil.JobResult(context.Background(), engine, jobKey)
	require.NoError(t, err)
	outRaw, err := resultData.GetData()
	require.NoError(t, err)
	t.Logf("raw job result: %q", string(outRaw))
	var outputs map[string]interface{}
	require.NoError(t, json.Unmarshal(outRaw, &outputs))
	t.Logf("final outputs: %#v", outputs)
	require.Equal(t, true, outputs["second"])
}

func makeTwoCommitRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "git", "init")
	runGitCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "git", "config", "user.name", "tester")
	writeFile(t, dir, "a.txt", "one")
	runGitCmd(t, dir, "git", "add", ".")
	runGitCmd(t, dir, "git", "commit", "-m", "first")
	writeFile(t, dir, "b.txt", "two")
	runGitCmd(t, dir, "git", "add", ".")
	runGitCmd(t, dir, "git", "commit", "-m", "second")
	hashBytes, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
	require.NoError(t, err, "rev-parse")
	return dir, strings.TrimSpace(string(hashBytes))
}

func cloneRepo(src string) (string, error) {
	dst, err := os.MkdirTemp("", "capability-worktree-*")
	if err != nil {
		return "", err
	}
	if err := runGitCmdErr(dst, "git", "clone", src, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func runGitCmd(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	if err := runGitCmdErr(dir, name, args...); err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
}

func runGitCmdErr(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v failed: %w (%s)", args, err, out)
	}
	return nil
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file failed: %v", err)
	}
}

type loggingTaskWorker struct {
	inner   swf.TaskWorker
	capture func(workerops.ActivityInvocationOutput, workerDebug)
}

type loggingJobWorker struct {
	inner swf.JobWorker
	after func(error)
}

func (l loggingJobWorker) Name() string {
	return l.inner.Name()
}

func (l loggingJobWorker) Run(ctx swf.JobContext, data swf.JobData) (swf.JobData, error) {
	var (
		out swf.JobData
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
			}
		}()
		out, err = l.inner.Run(ctx, data)
	}()
	if l.after != nil {
		l.after(err)
	}
	return out, err
}

func (l loggingTaskWorker) Name() string {
	return l.inner.Name()
}

func (l loggingTaskWorker) Run(ctx swf.TaskContext, input swf.TaskData) (swf.TaskData, error) {
	dbg := extractWorkerDebug(l.inner)
	out, err := l.inner.Run(ctx, input)
	if err != nil {
		return out, err
	}
	if l.capture != nil {
		data, err := out.GetData()
		if err == nil {
			var env workerops.ActivityInvocationOutput
			if json.Unmarshal(data, &env) == nil {
				l.capture(env, dbg)
			}
		}
	}
	return out, nil
}

type workerDebug struct {
	next         string
	workerType   string
	hasRegField  bool
	canInterface bool
}

func extractWorkerDebug(tw swf.TaskWorker) workerDebug {
	dbg := workerDebug{workerType: fmt.Sprintf("%T", tw)}
	rv := reflect.ValueOf(tw)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() == reflect.Struct {
		field := rv.FieldByName("reg")
		if field.IsValid() {
			dbg.hasRegField = true
			dbg.canInterface = field.CanInterface()
			if field.CanInterface() {
				if reg, ok := field.Interface().(workerops.ActivityRegistration); ok {
					dbg.next = reg.NextTaskType
				}
			}
		}
	}
	return dbg
}
