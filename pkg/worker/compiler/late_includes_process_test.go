package compiler

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type lateProcessRuntime struct {
	jobdb.WorkflowRuntime
	phase, marker string
}

func (r *lateProcessRuntime) PutChapter(ctx context.Context, req jobdb.PutChapterRequest) error {
	if err := r.WorkflowRuntime.PutChapter(ctx, req); err != nil {
		return err
	}
	if lateResultPhase(req.Chapter) == r.phase {
		if err := os.WriteFile(r.marker, []byte("accepted"), 0600); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

type lateUnacceptedCaptureBarrier struct {
	jobworkflow.TaskWorker
	marker string
}

func (w lateUnacceptedCaptureBarrier) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w lateUnacceptedCaptureBarrier) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := coretask.RunLocal(w.TaskWorker, ctx, tc, in)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(w.marker, []byte("prepared but unaccepted"), 0600); err != nil {
		return nil, err
	}
	<-ctx.Done()
	return out, ctx.Err()
}

func TestLateIncludeProcessExecutor(t *testing.T) {
	db := os.Getenv("C2J_LATE_PROCESS_DB")
	if db == "" {
		t.Skip("child-process executor")
	}
	registerCatchOp(t, "scope_probe")
	rt, err := sqliteruntime.NewFromConfig(testBackground(), sqliteruntime.Config{DBPath: db})
	require.NoError(t, err)
	defer rt.Close(testBackground())
	raw, err := os.ReadFile(os.Getenv("C2J_LATE_PROCESS_RECIPE"))
	require.NoError(t, err)
	rec, err := recipe.LoadInternalRecipeFromString(raw)
	require.NoError(t, err)
	var key jobdb.JobKey
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("C2J_LATE_PROCESS_KEY")), &key))
	var transports map[string]string
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("C2J_LATE_PROCESS_TRANSPORTS")), &transports))
	for k, v := range transports {
		lateTransports.Store(k, v)
	}
	inputs := func() map[string]any {
		raw, err := os.ReadFile(os.Getenv("C2J_LATE_PROCESS_INPUTS"))
		require.NoError(t, err)
		var v map[string]any
		require.NoError(t, json.Unmarshal(raw, &v))
		return v
	}
	phase := os.Getenv("C2J_LATE_PROCESS_PHASE")
	marker := os.Getenv("C2J_LATE_PROCESS_MARKER")
	interrupted := &lateProcessRuntime{WorkflowRuntime: rt, phase: phase, marker: marker}
	guard := lateGuard(interrupted)
	_, git := GenerateTestContext()
	probe := &scopeProbeTask{calls: map[string]int{}, git: git}
	tasks := lateTasks(t, guard, probe)
	if phase == "unaccepted-capture" {
		// The actual Git loader and artifact producer run before this barrier. Only
		// outcome acceptance is withheld, so the fresh executor must prepare again.
		resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{})
		for i, w := range tasks {
			if w.Name() == lateCaptureTaskType {
				tasks[i] = guard.WrapTaskWorker(lateUnacceptedCaptureBarrier{TaskWorker: lateCaptureWorker{resolver: resolver}, marker: marker})
			}
		}
	}
	h, err := jobworkflow.GetJobForRun(lateTestContext(), guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: lateWorker(t, *rec, inputs), TaskWorkers: tasks, WorkerID: "doomed-process", LeaseDuration: 500 * time.Millisecond})
	require.NoError(t, err)
	_, err = h.Run(nil)
	require.NoError(t, err)
	t.Fatal("parent must kill this executor at its barrier")
}
func TestLateIncludeProcessLoss(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, phase := range []string{"select", "capture", "unaccepted-capture"} {
		t.Run(phase, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: selected-v2\nversion: '9'\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {ok: true}\n")
			rec := f.expand(t, "id: root\ninput_schema:\n target: {type: string, required: true}\ninputs: {target: '${{ inputs.target }}'}\nsequence:\n- id: call\n  include: {bind: late, recipe: '${{ inputs.target }}'}\n")
			dir := t.TempDir()
			db := filepath.Join(dir, "jobs.db")
			recipePath := filepath.Join(dir, "recipe.yaml")
			inputPath := filepath.Join(dir, "inputs.json")
			marker := filepath.Join(dir, "accepted")
			bytes, err := marshalRecipeYAML(rec)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(recipePath, bytes, 0600))
			inputBytes, _ := json.Marshal(map[string]any{"target": f.selector("child.yaml", commit)})
			require.NoError(t, os.WriteFile(inputPath, inputBytes, 0600))
			rt, err := sqliteruntime.NewFromConfig(testBackground(), sqliteruntime.Config{DBPath: db})
			require.NoError(t, err)
			defer rt.Close(testBackground())
			w := lateWorker(t, rec, func() map[string]any { return map[string]any{"target": "changed-invalid-selector"} })
			key := probeSubmit(t, rt, w)
			// Compare recovery with a complete native execution of the same
			// authored call and source, using its own job and empty cache.
			controlWorker := lateWorker(t, rec, func() map[string]any { return map[string]any{"target": f.selector("child.yaml", commit)} })
			controlKey := probeSubmit(t, rt, controlWorker)
			controlGuard := lateGuard(rt)
			_, controlGit := GenerateTestContext()
			controlProbe := &scopeProbeTask{calls: map[string]int{}, git: controlGit}
			control := lateRun(t, controlGuard, controlKey, controlWorker, lateTasks(t, controlGuard, controlProbe)...)
			require.Equal(t, jobworkflow.JobRunCompleted, control.Status, "%v", control.JobError)
			controlS, controlC, _ := lateRecordOutputs(t, rt, controlKey)
			require.Len(t, controlC, 1)

			keyJSON, _ := json.Marshal(key)
			transports := map[string]string{}
			lateTransports.Range(func(k, v any) bool { transports[k.(string)] = v.(string); return true })
			transportJSON, _ := json.Marshal(transports)
			executable, err := os.Executable()
			require.NoError(t, err)
			cmd := exec.Command(executable, "-test.run=^TestLateIncludeProcessExecutor$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "C2J_LATE_PROCESS_DB="+db, "C2J_LATE_PROCESS_KEY="+string(keyJSON), "C2J_LATE_PROCESS_RECIPE="+recipePath, "C2J_LATE_PROCESS_INPUTS="+inputPath, "C2J_LATE_PROCESS_MARKER="+marker, "C2J_LATE_PROCESS_PHASE="+phase, "C2J_LATE_PROCESS_TRANSPORTS="+string(transportJSON))
			log, err := os.Create(filepath.Join(dir, "child.log"))
			require.NoError(t, err)
			defer log.Close()
			cmd.Stdout = log
			cmd.Stderr = log
			require.NoError(t, cmd.Start())
			defer func() { _ = cmd.Process.Kill() }()
			require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 15*time.Second, 20*time.Millisecond, "child log: %s", filepath.Join(dir, "child.log"))
			s, c, _ := lateRecordOutputs(t, rt, key)
			require.Len(t, s, 1)
			require.Equal(t, commit, s[0].Commit)
			if phase == "capture" {
				require.Len(t, c, 1)
			} else {
				require.Empty(t, c)
			}
			require.NoError(t, cmd.Process.Kill())
			require.Error(t, cmd.Wait())
			time.Sleep(650 * time.Millisecond) // The killed process can no longer renew its short lease.
			before := f.reads.Load()
			if phase == "capture" {
				f.available.Store(false)
				require.NoError(t, os.RemoveAll(f.dir))
			}
			guard := lateGuard(rt)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, git: git}
			outcome := lateRun(t, guard, key, w, lateTasks(t, guard, probe)...)
			require.Equal(t, jobworkflow.JobRunCompleted, outcome.Status, "%v", outcome.JobError)
			afterS, afterC, _ := lateRecordOutputs(t, rt, key)
			require.Equal(t, s, afterS)
			require.Len(t, afterC, 1)
			require.Equal(t, controlS, afterS)
			require.Equal(t, controlC[0].ContentSHA256, afterC[0].ContentSHA256)
			require.Equal(t, controlProbe.calls, probe.calls)

			require.Equal(t, map[string]int{"body": 1}, probe.calls)
			if phase == "capture" {
				require.Equal(t, before, f.reads.Load())
			} else {
				require.Greater(t, f.reads.Load(), before)
			}
			t.Logf("persistent SQLite process loss after %s: selection=%s captures=%d body effects=%d", phase, s[0].Invocation, len(afterC), probe.calls["body"])
		})
	}
}
