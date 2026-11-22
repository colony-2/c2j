package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeMissingAndCorruptStoredCaptures(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, mode := range []string{"missing", "corrupt", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: captured\ndesc: "+strings.Repeat("large capture ", 40000)+"\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\n")
			rec := f.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n", f.selector("child.yaml", commit)))
			dir := t.TempDir()
			blobDir := filepath.Join(dir, "blobs")
			rt, err := sqliteruntime.NewFromConfig(testBackground(), sqliteruntime.Config{DBPath: filepath.Join(dir, "jobs.db"), BlobDir: blobDir})
			require.NoError(t, err)
			defer rt.Close(testBackground())
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			ctx, cancel := context.WithCancel(lateTestContext())
			defer cancel()
			interruption := &lateInterruption{ConsumerRuntime: rt, phase: "capture", cancel: cancel}
			guard := lateGuard(interruption)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, git: git}
			first, err := jobworkflow.GetJobForRun(ctx, guard, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: w, TaskWorkers: lateTasks(t, guard, probe), WorkerID: "first", LeaseDuration: time.Minute})
			require.NoError(t, err)
			_, err = first.Run(nil)
			require.Error(t, err)
			require.True(t, interruption.fired)
			s, c, run := lateRecordOutputs(t, rt, key)
			require.Len(t, s, 1)
			require.Len(t, c, 1)
			require.Empty(t, probe.calls)
			saved := lateArtifactBytes(t, rt, key, run)
			require.Len(t, saved, 1)
			require.Greater(t, len(saved[0]), 400000)
			chapters, err := rt.ListChapters(testBackground(), jobdb.ListChaptersRequest{JobKey: key})
			require.NoError(t, err)
			for _, ch := range chapters {
				require.NoError(t, jobdb.ValidateChapterApplicationPayload(testBackground(), ch, 0))
			}
			for _, e := range run.Execution {
				if e.Record.Output != nil && strings.Contains(string(e.Record.Output.Data), "recipe_yaml_artifact") {
					require.Less(t, len(e.Record.Output.Data), 4096)
					require.NotEmpty(t, e.Record.Output.Artifacts)
				}
			}
			found := ""
			require.NoError(t, filepath.WalkDir(blobDir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if bytes.Equal(data, saved[0]) {
					found = path
				}
				return nil
			}))
			require.NotEmpty(t, found, "find actual persisted definition blob")
			if mode == "missing" {
				require.NoError(t, os.Remove(found))
			} else if mode == "unreadable" {
				require.NoError(t, os.Remove(found))
				require.NoError(t, os.Mkdir(found, 0700))
			} else {
				altered := append([]byte(nil), saved[0]...)
				altered[len(altered)/2] ^= 1
				require.NoError(t, os.WriteFile(found, altered, 0600))
			}
			f.available.Store(false)
			reads := f.reads.Load()
			guard = lateGuard(rt)
			lateRunInfrastructureFailure(t, guard, key, w, lateTasks(t, guard, probe)...)
			require.Empty(t, probe.calls)
			require.Equal(t, reads, f.reads.Load())
		})
	}
}

// Fault injection transforms a real Git-backed capture, then lets the native
// backend store the damaged artifact/metadata. It never supplies a fake success.
type lateDamagedCaptureWorker struct {
	jobworkflow.TaskWorker
	mode  string
	after func()
}

func (w lateDamagedCaptureWorker) Run(tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	return w.RunTask(tc.Context(), tc, in)
}
func (w lateDamagedCaptureWorker) RunTask(ctx context.Context, tc jobworkflow.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	out, err := coretask.RunLocal(w.TaskWorker, ctx, tc, in)
	if err != nil {
		return nil, err
	}
	var c lateCapture
	if err = json.Unmarshal(out.GetDataOrPanic(), &c); err != nil {
		return nil, err
	}
	art, err := findResolvedRecipeYAMLArtifact(out, c.RecipeYAMLArtifact)
	if err != nil {
		return nil, err
	}
	raw, err := art.Bytes(ctx)
	if err != nil {
		return nil, err
	}
	if w.after != nil {
		w.after()
	}
	switch w.mode {
	case "cel-metadata":
		c.Functions = WithinRecipeResolutionResult{}
	case "cel-missing-inventory", "cel-duplicate", "cel-file", "cel-pin":
		r, err := recipe.LoadInternalRecipeFromString(raw)
		if err != nil {
			return nil, err
		}
		packages := r.GetMetadata().Internal.FunctionPackages
		switch w.mode {
		case "cel-missing-inventory":
			r.GetMetadata().Internal.FunctionPackages = nil
		case "cel-duplicate":
			r.GetMetadata().Internal.FunctionPackages = append(packages, packages[0])
		case "cel-file":
			packages[0].Files["tools/functions/functions.yaml"] = "invalid-base64!"
		case "cel-pin":
			packages[0].ResolvedCommit = strings.Repeat("f", 40)
		}
		raw, err = marshalRecipeYAML(*r)
		if err != nil {
			return nil, err
		}
		c.ContentSHA256 = sha256Hex(raw)
	case "missing-reference":
		return jobdb.NewTaskData(c)
	case "duplicate-reference":
		return jobdb.NewTaskData(c, art, art)
	case "selection-mismatch":
		c.Selection.Commit = strings.Repeat("a", 40)
	case "source-mismatch":
		r, err := recipe.LoadInternalRecipeFromString(raw)
		if err != nil {
			return nil, err
		}
		r.GetMetadata().Internal.DefinitionSource.ResolvedCommit = strings.Repeat("a", 40)
		raw, err = marshalRecipeYAML(*r)
		if err != nil {
			return nil, err
		}
		c.ContentSHA256 = sha256Hex(raw)
	case "malformed-definition":
		raw = []byte("not: [a recipe\n")
		c.ContentSHA256 = sha256Hex(raw)
	case "digest-mismatch":
		c.ContentSHA256 = strings.Repeat("0", 64)
	}
	return jobdb.NewTaskData(c, jobdb.NewArtifactFromBytes(c.RecipeYAMLArtifact, raw))
}
func TestLateIncludeRejectsDamagedCaptureContracts(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, mode := range []string{"missing-reference", "duplicate-reference", "selection-mismatch", "source-mismatch", "malformed-definition", "digest-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: captured\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\n")
			rec := f.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n", f.selector("child.yaml", commit)))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, git: git}
			tasks := lateTasks(t, guard, probe)
			// Keep the actual loader and native artifact storage, inserting only damage.
			resolver := NewRecipeSourceResolver(RecipeSourceResolverOptions{})
			for i, task := range tasks {
				if task.Name() == lateCaptureTaskType {
					tasks[i] = guard.WrapTaskWorker(lateDamagedCaptureWorker{TaskWorker: lateCaptureWorker{resolver: resolver}, mode: mode})
				}
			}
			if mode == "duplicate-reference" {
				// Duplicate attachments are rejected while constructing the task
				// result, before a capture is persisted. This remains validation.
				out := lateRun(t, guard, key, w, tasks...)
				require.Equal(t, jobworkflow.JobRunFailed, out.Status)
			} else {
				lateRunInfrastructureFailure(t, guard, key, w, tasks...)
			}
			require.Empty(t, probe.calls)
			require.Greater(t, f.reads.Load(), int32(0))
			s, _, _ := lateRecordOutputs(t, rt, key)
			require.Len(t, s, 1)
		})
	}
}

func TestLateIncludeDamagedCELCaptureDoesNotRefresh(t *testing.T) {
	registerCatchOp(t, "scope_probe")
	for _, mode := range []string{"cel-metadata", "cel-missing-inventory", "cel-duplicate", "cel-file", "cel-pin"} {
		t.Run(mode, func(t *testing.T) {
			f := newLateGitFixture(t)
			writeFunctionPackage(t, filepath.Join(f.dir, "tools", "functions"), "name: markers\nfunctions:\n- name: marker\n  mode: function\n  execution: sh marker.sh\n  args: [{name: value, schema: {type: string}}]\n  returns: {schema: {type: string}}\n", map[string]string{"marker.sh": "cat"})
			commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nextensions:\n functions: [{selector: ./tools/functions, include: [marker]}]\nsequence:\n- id: effect\n  op: scope_probe\n  inputs: {name: body}\noutputs: {value: '${{ marker(\"body\") }}'}\n")
			rec := f.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\n", f.selector("child.yaml", commit)))
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			_, git := GenerateTestContext()
			probe := &scopeProbeTask{calls: map[string]int{}, git: git}
			tasks := lateTasks(t, guard, probe)
			reads := int32(0)
			for i, task := range tasks {
				if task.Name() == lateCaptureTaskType {
					tasks[i] = guard.WrapTaskWorker(lateDamagedCaptureWorker{TaskWorker: lateCaptureWorker{}, mode: mode, after: func() { reads = f.reads.Load(); f.available.Store(false) }})
				}
			}
			lateRunInfrastructureFailure(t, guard, key, w, tasks...)
			require.Empty(t, probe.calls)
			require.Greater(t, reads, int32(0))
			require.Equal(t, reads, f.reads.Load())
			s, c, _ := lateRecordOutputs(t, rt, key)
			require.Len(t, s, 1)
			require.Len(t, c, 1)
		})
	}
}

// Corrupt retained capture protocols stop this invocation. They cannot become
// a stored recipe failure or trigger a refresh of the original source.
func lateRunInfrastructureFailure(t *testing.T, rt jobdb.ConsumerRuntime, key jobdb.JobKey, worker jobworkflow.ExecutionWorker, tasks ...jobworkflow.TaskWorker) {
	t.Helper()
	h, err := jobworkflow.GetJobForRun(lateTestContext(), rt, jobworkflow.GetJobForRunRequest{JobKey: key, JobWorker: worker, TaskWorkers: tasks, WorkerID: "late-tests", LeaseDuration: time.Minute})
	require.NoError(t, err)
	out, err := h.Run(nil)
	require.True(t, jobdb.IsExecutionInfrastructureError(err), "%v", err)
	require.NotEqual(t, jobworkflow.JobRunFailed, out.Status)
	info, err := rt.GetJob(lateTestContext(), key)
	require.NoError(t, err)
	require.NotEqual(t, jobdb.JobStatusCompleted, info.Status)
	require.Empty(t, info.CompletionStatus)
}
