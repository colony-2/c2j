package compiler

import (
	"fmt"
	"strings"
	"testing"

	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeGitSelectionAndArtifacts(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "flows/child.yaml", "id: selected\nversion: '2'\nsequence: []\noutputs: {answer: captured}\n")
			rec := f.expand(t, fmt.Sprintf("id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: %q}\noutputs: {answer: '{{ sequence.call.outputs.answer }}'}\n", f.selector("flows/./child.yaml", strings.ToUpper(commit))))
			require.Zero(t, f.reads.Load())
			rt := newTimeoutReproRuntime(t, backend)
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			out := lateRun(t, guard, key, w, lateTasks(t, guard)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			selections, captures, run := lateRecordOutputs(t, rt, key)
			require.Len(t, selections, 1)
			require.Len(t, captures, 1)
			require.Equal(t, commit, selections[0].Commit)
			require.Equal(t, "flows/child.yaml", selections[0].Path)
			require.Equal(t, f.url, selections[0].Repository)
			require.False(t, selections[0].PermissionBoundary)
			require.True(t, selections[0].TrustedSource)
			bytes := lateArtifactBytes(t, rt, key, run)
			require.Len(t, bytes, 1)
			require.Equal(t, captures[0].ContentSHA256, sha256Hex(bytes[0]))
			require.Contains(t, string(bytes[0]), "definition_source:")
			f.available.Store(false)
			before := f.reads.Load()
			require.NoError(t, probeReplay(t, rt, key, w))
			require.Equal(t, before, f.reads.Load())
		})
	}
}
func TestLateIncludeRuntimeTargetRejectionsAndSkip(t *testing.T) {
	for _, state := range []bool{false, true} {
		for _, selector := range []string{"git+https://example.test/repo//child.yaml@main", "git+https://example.test/repo//child.yaml@abcdef0", "git+https://example.test/repo//child.yaml@" + strings.Repeat("z", 40), "./child.yaml", "git+file:///tmp/local.git//child.yaml@" + strings.Repeat("a", 40), "bare", "cell://.c2/hooks/a.yaml", "git+https://example.test/repo//../escape@" + strings.Repeat("a", 40), "${{ 42 }}"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, selector), func(t *testing.T) {
				f := newLateGitFixture(t)
				call := fmt.Sprintf("include: {bind: late, recipe: %q}\n", "${{ inputs.target }}")
				body := "id: root\ninput_schema:\n target: {type: string, required: true}\ninputs: {target: '{{ inputs.target }}'}\nsequence:\n- id: call\n  " + call
				if state {
					body = "id: root\ninput_schema:\n target: {type: string, required: true}\ninputs: {target: '{{ inputs.target }}'}\nstate:\n initial: call\n states:\n  call:\n   " + call
				}
				rec := f.expand(t, body)
				rt := newTimeoutReproRuntime(t, "sqlite")
				guard := lateGuard(rt)
				w := lateWorker(t, rec, func() map[string]any { return map[string]any{"target": selector} })
				key := probeSubmit(t, rt, w)
				out := lateRun(t, guard, key, w, lateTasks(t, guard)...)
				require.Equal(t, jobworkflow.JobRunFailed, out.Status)
				require.Zero(t, f.reads.Load())
				s, c, _ := lateRecordOutputs(t, rt, key)
				require.Empty(t, s)
				require.Empty(t, c)
			})
		}
	}
	for _, state := range []bool{false, true} {
		t.Run(fmt.Sprintf("skipped/state=%t", state), func(t *testing.T) {
			f := newLateGitFixture(t)
			body := "id: root\nsequence:\n- id: call\n  when: false\n  include: {bind: late, recipe: '${{ missing_selector() }}'}\n"
			if state {
				body = "id: root\nstate:\n initial: call\n states:\n  call:\n   when: false\n   include: {bind: late, recipe: '${{ missing_selector() }}'}\n"
			}
			rec := f.expand(t, body)
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			out := lateRun(t, guard, key, w, lateTasks(t, guard)...)
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Zero(t, f.reads.Load())
			s, c, _ := lateRecordOutputs(t, rt, key)
			require.Empty(t, s)
			require.Empty(t, c)
		})
	}
}

func TestLateIncludeSelectionUsesCallerBeforeCalleeEntry(t *testing.T) {
	for _, expression := range []string{"${{ vars.target }}", "git+{{ vars.repository }}//{{ vars.path }}@{{ vars.commit }}", "${{ 42 }}"} {
		t.Run(expression, func(t *testing.T) {
			f := newLateGitFixture(t)
			commit := commitHookFile(t, f.dir, "child.yaml", "id: child\nvars: {target: callee}\nsequence: []\noutputs: {value: '${{ vars.target }}'}\n")
			body := fmt.Sprintf("id: root\nvars: {target: %q, repository: %q, path: child.yaml, commit: %q}\nsequence:\n- id: call\n  vars: {target: invalid-local-target}\n  include: {bind: late, recipe: %q}\noutputs: {value: '${{ sequence.call.outputs.value }}'}\n", f.selector("child.yaml", commit), f.url, commit, expression)
			rec := f.expand(t, body)
			rt := newTimeoutReproRuntime(t, "sqlite")
			guard := lateGuard(rt)
			w := lateWorker(t, rec, nil)
			key := probeSubmit(t, rt, w)
			out := lateRun(t, guard, key, w, lateTasks(t, guard)...)
			s, c, _ := lateRecordOutputs(t, rt, key)
			if expression == "${{ 42 }}" {
				require.Equal(t, jobworkflow.JobRunFailed, out.Status)
				require.ErrorContains(t, out.JobError, "must evaluate to a string")
				require.Empty(t, s)
				require.Empty(t, c)
				require.Zero(t, f.reads.Load())
			} else {
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Equal(t, "callee", lateJobOutput(t, rt, key)["value"])
				require.Len(t, s, 1)
				require.Equal(t, commit, s[0].Commit)
			}
		})
	}
}
