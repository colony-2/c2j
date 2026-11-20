package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/c2j/pkg/git/common"
	"github.com/colony-2/c2j/pkg/git/squashrebasemerge"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	coreops "github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type savedWriterOracle struct {
	jobdb.ConsumerRuntime
	mu    sync.Mutex
	bytes map[string][]byte
}

func (r *savedWriterOracle) CreateDelegation(ctx context.Context, parent jobdb.ExecutionLease, q jobdb.CreateDelegationRequest) (jobdb.RenewableExecutionLease, error) {
	if d, ok := r.ConsumerRuntime.(jobdb.LeaseDelegator); ok {
		return d.CreateDelegation(ctx, parent, q)
	}
	return nil, jobdb.ErrLeaseDelegationUnsupported
}

func (r *savedWriterOracle) PutChapter(ctx context.Context, q jobdb.PutChapterRequest) error {
	for _, a := range q.ArtifactUploads {
		if a.Name == "diff_from_base.diff" || a.Name == "diff_from_parent.diff" {
			body, err := a.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(body)
			err = errors.Join(err, body.Close())
			if err != nil {
				return err
			}
			r.mu.Lock()
			r.bytes[q.Ref.JobKey.JobId+"/"+strconv.FormatInt(q.Ref.Ordinal, 10)+"/"+a.Name] = data
			r.mu.Unlock()
		}
	}
	return r.ConsumerRuntime.PutChapter(ctx, q)
}
func savedPointer(s string) *string { return &s }
func savedCompilerRead(t *testing.T, ctx context.Context, api *jobinspect.Service, oracle *savedWriterOracle, q jobinspect.WorkspaceDiffRequest) *jobinspect.WorkspaceDiff {
	t.Helper()
	server, err := childbroker.Start(jobinspect.WithAPI(ctx, api), childbroker.Options{Current: jobcontext.Current{TenantID: api.Tenant, JobID: q.JobID, GoalID: "saved"}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, err)
	defer server.Close()
	broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
	require.NoError(t, err)
	var m *jobinspect.WorkspaceDiff
	for _, surface := range []jobinspect.WorkspaceDiffAPI{api, childbroker.JobClient{Broker: broker}, workspacedifftest.CLI{Binary: workspacedifftest.PrepareCLI(t), Env: server.Env()}} {
		got, body, err := surface.OpenWorkspaceDiff(ctx, q)
		require.NoError(t, err)
		require.NoError(t, jobinspect.ValidateWorkspaceDiff(q, got))
		require.NotEqual(t, "unavailable", got.Status, "%+v", got.Reasons)
		require.NotNil(t, body)
		data, err := io.ReadAll(body)
		require.NoError(t, err)
		require.NoError(t, body.Close())
		if got.Source != nil {
			want, ok := oracle.bytes[got.Source.Key.JobId+"/"+strconv.FormatInt(got.Source.Key.TaskOrdinal, 10)+"/"+got.Source.Key.Name]
			require.True(t, ok, "oracle captured the writer file before persistence/cleanup")
			require.Equal(t, want, data)
		} else {
			require.Empty(t, data)
		}
		if m != nil {
			require.Equal(t, m, got)
		}
		m = got
	}

	return m
}
func TestWorkspaceDiffCompilerS03S05S10S15S16F02Boundaries(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, backend)
			oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			workspaceTestWorker(t)
			key := submitGitLifecycle(t, rt, repo, `id: saved-boundaries
version: "1"
sequence:
 - id: first
   op: workspace_probe
   inputs: {write: first.txt}
 - id: second
   op: workspace_probe
   inputs: {write: second.txt}
 - id: nested
   sequence:
    - id: third
      op: workspace_probe
      inputs: {write: third.txt}
    - id: unchanged
      op: workspace_probe
      const: true
 - id: continuation
   op: workspace_probe
   const: true
`)
			out, err := runGitLifecycle(t, oracle, key)
			require.NoError(t, err)
			gitLifecycleOutput(t, out)
			guard := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
			api := &jobinspect.Service{Runtime: guard, Tenant: key.TenantId}
			before, err := changestest.Inventory(ctx, rt, key)
			require.NoError(t, err)
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var second, third string
			for _, task := range log.Tasks {
				if strings.HasSuffix(task.Path, "/second") {
					second = task.ID
				}
				if strings.HasSuffix(task.Path, "/third") {
					third = task.ID
				}
				if strings.HasSuffix(task.Path, "/first") {
					q := jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &task.ID, Boundary: savedPointer("before")}}
					m := savedCompilerRead(t, ctx, api, oracle, q)
					require.Equal(t, "empty", m.Status)
					require.Nil(t, m.Source)
					q.Boundary = nil
					m, body, err := api.OpenWorkspaceDiff(ctx, q)
					require.NoError(t, err)
					require.Equal(t, "unavailable", m.Status, "generic task context cannot prove a parent-only starting endpoint")
					require.Nil(t, body)
				}
			}
			require.NotEmpty(t, second)
			require.NotEmpty(t, third)
			// A forbidden Git executable also catches accidental reconstruction while
			// inspecting these completed native fixtures.
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 93\n"), 0700))
			t.Setenv("PATH", bin)
			latest := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId})
			require.Equal(t, third, latest.Source.Producer.Task)
			require.NotEqual(t, third, latest.StateProducer.Task)
			story, err := api.Inspect(ctx, jobinspect.Request{Operation: "story", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var scope string
			for _, sc := range story.Scopes {
				if strings.HasSuffix(sc.ID, `scope:"sequence:nested"#1`) {
					scope = sc.ID
					require.Len(t, sc.Attempts, 1)
					require.True(t, sc.Attempts[0].Before)
					require.True(t, sc.Attempts[0].After)
				}
			}
			require.NotEmpty(t, scope)
			for _, boundary := range []string{"before", "after"} {
				q := jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Scope: &scope, Boundary: &boundary}}
				m := savedCompilerRead(t, ctx, api, oracle, q)
				want := third
				if boundary == "before" {
					want = second
				}
				require.Equal(t, want, m.Source.Producer.Task)
			}
			selected := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &second}})
			require.Equal(t, second, selected.Source.Producer.Task)
			require.NotEqual(t, latest.Workspace.HeadCommit, selected.Workspace.HeadCommit)
			after, err := changestest.Inventory(ctx, rt, key)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Zero(t, guard.Mutations.Load())
		})
	}
}

func TestWorkspaceDiffCompilerS11S13S14S19RetryIsolation(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, backend)
			oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
			calls := 0
			flaky := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "saved_flaky", Version: "1.0.0"}, func(_ coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				calls++
				if calls == 1 {
					return nil, errors.New("retry this native scope")
				}
				return map[string]any{"ok": true}, nil
			})
			workspaceTestWorker(t, flaky)
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: saved-retry
version: "1"
workspace: {cell: test, repo: %q}
sequence:
 - id: prefix1
   op: workspace_probe
   inputs: {write: prefix1.txt}
 - id: prefix2
   op: workspace_probe
   inputs: {write: prefix2.txt}
 - id: block
   retry: {maximum_attempts: 2, initial_interval: 1ms}
   sequence:
    - id: changed
      op: workspace_probe
      inputs: {write: changed.txt}
    - id: flaky
      op: saved_flaky
 - id: suffix
   op: workspace_probe
   const: true
`, celltest.Repo(repo)))
			out, err := runGitLifecycle(t, oracle, key)
			require.NoError(t, err)
			gitLifecycleOutput(t, out)
			require.Equal(t, 2, calls)
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var points []string
			for _, task := range log.Tasks {
				if strings.HasSuffix(task.Path, "/changed") {
					points = append(points, task.ID)
				}
			}
			require.Len(t, points, 2)
			require.NotEqual(t, points[0], points[1])
			for _, id := range points {
				m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &id}})
				require.Equal(t, id, m.Resolved.Task)
				require.Equal(t, id, m.StateProducer.Task)
				require.Equal(t, points[0], m.Source.Producer.Task, "recorded incoming snapshot carries the original bytes across an unchanged retry")
			}
			latest := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId})
			require.Equal(t, points[0], latest.Source.Producer.Task)
			require.NotEqual(t, points[0], latest.StateProducer.Task)
			story, err := api.Inspect(ctx, jobinspect.Request{Operation: "story", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			found := false
			for _, sc := range story.Scopes {
				if strings.HasSuffix(sc.ID, `scope:"sequence:block"#1`) {
					found = true
					require.Len(t, sc.Attempts, 2)
					require.Equal(t, 1, sc.Attempts[0].Attempt)
					require.Equal(t, "failed", sc.Attempts[0].Status)
					require.Equal(t, 2, sc.Attempts[1].Attempt)
					for _, n := range []int{1, 2} {
						m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Scope: &sc.ID, Attempt: &n}})
						require.Equal(t, n, m.Resolved.Attempt)
						if n == 1 {
							require.Equal(t, "diagnostic", m.Resolved.Status)
						}
					}
					m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Scope: &sc.ID}})
					require.Equal(t, 2, m.Resolved.Attempt)
				}
			}
			require.True(t, found)
		})
	}
}

func TestWorkspaceDiffCompilerP02SquashParentProof(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, emptyBase := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/emptyBase=%t", backend, emptyBase), func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				rt := newTimeoutReproRuntime(t, backend)
				oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
				workspaceTestWorker(t, squashrebasemerge.GetSquashOp())
				repo := filepath.Join(t.TempDir(), "repo")
				workspaceRepo(t, repo, "source")
				workspace := ""
				if emptyBase {
					workspace = fmt.Sprintf("workspace: {cell: test, repo: %q, initial: empty}\n", celltest.Repo(repo))
				}
				key := submitGitLifecycle(t, rt, repo, `id: saved-squash
version: "1"
`+workspace+`sequence:
 - id: first
   op: workspace_probe
   inputs: {write: first.txt}
 - id: second
   op: workspace_probe
   inputs: {write: second.txt}
 - id: squash
   op: squash
`)
				out, err := runGitLifecycle(t, oracle, key)
				require.NoError(t, err)
				gitLifecycleOutput(t, out)
				api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
				m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId})
				require.Equal(t, "diff_from_parent.diff", m.Source.Key.Name)
				require.Equal(t, emptyBase, m.Workspace.EmptyBase)
				if emptyBase {
					require.Empty(t, m.Workspace.BaseCommit)
				}
			})
		}
	}
}

func TestWorkspaceDiffCompilerS03P03P06Rebase(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed=%t", backend, changed), func(t *testing.T) {
				ctx := celltest.Context(context.Background())
				rt := newTimeoutReproRuntime(t, backend)
				oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
				repo := filepath.Join(t.TempDir(), "repo")
				initial := workspaceRepo(t, repo, "source")
				var target string
				advance := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "saved_advance"}, func(_ coreops.OpDependencies, c context.Context, _ struct{}) (map[string]any, error) {
					if err := os.WriteFile(filepath.Join(repo, "upstream"), []byte("advanced upstream"), 0600); err != nil {
						return nil, err
					}
					if err := runGit(repo, "git", "add", "."); err != nil {
						return nil, err
					}
					if err := runGit(repo, "git", "commit", "-m", "advance upstream"); err != nil {
						return nil, err
					}
					var err error
					target, err = common.GetCommitHash(c, repo, "HEAD")
					return map[string]any{"hash": target}, err
				})
				workspaceTestWorker(t, thinpackrebase.GetRebaseOp(), advance)
				prefix := ""
				if changed {
					prefix = " - id: first\n   op: workspace_probe\n   inputs: {write: first.txt}\n - id: second\n   op: workspace_probe\n   inputs: {write: second.txt}\n"
				}
				key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: saved-rebase
version: "1"
workspace: {cell: test, repo: %q}
sequence:
%s - id: advance
   op: saved_advance
 - id: reset
   op: rebase
   inputs: {target_base_hash: '${{ sequence.advance.outputs.hash }}'}
 - id: continued
   op: workspace_probe
   const: true
`, celltest.Repo(repo), prefix))
				out, err := runGitLifecycle(t, oracle, key)
				require.NoError(t, err)
				gitLifecycleOutput(t, out)
				api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}

				m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId})
				require.NotEqual(t, initial, target)
				require.Equal(t, target, m.Workspace.BaseCommit)
				if changed {
					require.NotEqual(t, target, m.Workspace.HeadCommit)
				} else {
					require.Equal(t, "empty", m.Status)
					require.Equal(t, target, m.Workspace.HeadCommit)
					require.Nil(t, m.Source)
				}
			})
		}
	}
}

func TestWorkspaceDiffCompilerS13RepeatedSites(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, backend)
			oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
			calls := 0
			tick := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "saved_tick", Version: "1.0.0"}, func(d coreops.OpDependencies, _ context.Context, _ struct{}) (map[string]any, error) {
				calls++
				err := os.WriteFile(filepath.Join(d.WorktreePath(), fmt.Sprintf("turn-%d", calls)), []byte("original loop\n"), 0600)
				return map[string]any{"count": calls}, err
			})
			workspaceTestWorker(t, tick)
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			key := submitGitLifecycle(t, rt, repo, `id: saved-loop
version: "1"
state:
 initial: warmup
 states:
  warmup:
   op: workspace_probe
   inputs: {write: warmup}
   transitions: [{to: cycle}]
  cycle:
   op: saved_tick
   transitions: [{to: cycle, when: 'states.cycle.outputs.count < 3'}]
`)
			out, err := runGitLifecycle(t, oracle, key)
			require.NoError(t, err)
			gitLifecycleOutput(t, out)
			require.Equal(t, 3, calls)
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			ids := map[string]bool{}
			heads := map[string]bool{}
			for _, task := range log.Tasks {
				if task.Type != "saved_tick:saved_tick" {
					continue
				}
				require.False(t, ids[task.ID])
				ids[task.ID] = true
				m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &task.ID}})
				require.Equal(t, task.ID, m.Source.Producer.Task)
				require.False(t, heads[m.Workspace.HeadCommit])
				heads[m.Workspace.HeadCommit] = true
			}
			require.Len(t, ids, 3)
		})
	}
}

type savedPendingRuntime struct{ jobdb.ConsumerRuntime }

func (*savedPendingRuntime) skipGitTask(name string) bool {
	return name == "saved_pending:saved_pending"
}

func TestWorkspaceDiffCompilerS15S17S18S19OpenScope(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, backend)
			oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
			pending := coreops.NewActivityMappedOpV2[struct{}, map[string]any](coreops.OpMetadata{Type: "saved_pending", Version: "1.0.0"}, func(coreops.OpDependencies, context.Context, struct{}) (map[string]any, error) {
				panic("pending worker must not run")
			})
			workspaceTestWorker(t, pending)
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			key := submitGitLifecycle(t, rt, repo, `id: saved-open
version: "1"
sequence:
 - id: first
   op: workspace_probe
   inputs: {write: first.txt}
 - id: block
   sequence:
    - id: second
      op: workspace_probe
      inputs: {write: second.txt}
    - id: pending
      op: saved_pending
`)
			out, err := runGitLifecycle(t, &savedPendingRuntime{oracle}, key)
			require.NoError(t, err)
			require.Equal(t, jobworkflow.JobRunSuspended, out.Status)
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var id string
			for _, task := range log.Tasks {
				if task.Type == "saved_pending:saved_pending" {
					id = task.ID
					require.Equal(t, "running", task.Status)
				}
			}
			require.NotEmpty(t, id)
			q := jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &id, Boundary: savedPointer("before")}}
			m := savedCompilerRead(t, ctx, api, oracle, q)
			require.Equal(t, "waiting", m.Resolved.Status)
			q.Boundary = savedPointer("after")
			m, body, err := api.OpenWorkspaceDiff(ctx, q)
			require.NoError(t, err)
			require.Equal(t, "unavailable", m.Status)
			require.Nil(t, body)
			story, err := api.Inspect(ctx, jobinspect.Request{Operation: "story", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var scope string
			for _, sc := range story.Scopes {
				if strings.HasSuffix(sc.ID, `scope:"sequence:block"#1`) {
					scope = sc.ID
					require.Equal(t, "in_progress", sc.Status)
				}
			}
			require.NotEmpty(t, scope)
			m = savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Scope: &scope}})
			require.Equal(t, "in_progress", m.Resolved.Status)
			require.NotEmpty(t, m.Observation)
			require.NotEmpty(t, m.Cutoff)
		})
	}
}

func TestWorkspaceDiffCompilerReviewFailedScope(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, backend)
			oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
			workspaceTestWorker(t)
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			key := submitGitLifecycle(t, rt, repo, `id: saved-failed
version: "1"
sequence:
 - id: broken
   sequence:
    - id: first
      op: workspace_probe
      inputs: {write: first.txt}
    - id: second
      op: workspace_probe
      inputs: {write: second.txt}
    - id: failed
      op: workspace_probe
      inputs: {write: failed.txt, fail: true}
`)
			out, err := runGitLifecycle(t, oracle, key)
			require.NoError(t, err)
			require.NotNil(t, out.JobError)
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var failed, second string
			for _, task := range log.Tasks {
				if strings.HasSuffix(task.Path, "/failed") {
					failed = task.ID
					require.Equal(t, "failed", task.Status)
				}
				if strings.HasSuffix(task.Path, "/second") {
					second = task.ID
				}
			}
			require.NotEmpty(t, failed)
			m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &failed}})
			require.Equal(t, "diagnostic", m.Resolved.Status)
			require.Equal(t, failed, m.Source.Producer.Task)
			story, err := api.Inspect(ctx, jobinspect.Request{Operation: "story", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var scope string
			for _, sc := range story.Scopes {
				if strings.HasSuffix(sc.ID, `scope:"sequence:broken"#1`) {
					scope = sc.ID
				}
			}
			require.NotEmpty(t, scope)
			for _, boundary := range []*string{nil, savedPointer("after")} {
				selected := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Scope: &scope, Boundary: boundary}})
				require.Equal(t, "diagnostic", selected.Resolved.Status)
				require.Equal(t, failed, selected.StateProducer.Task)
				require.Equal(t, m.Source, selected.Source)
				require.Equal(t, m.Workspace, selected.Workspace)
			}
			latest := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId})
			require.Equal(t, second, latest.Source.Producer.Task)
			require.NotEqual(t, m.Workspace.HeadCommit, latest.Workspace.HeadCommit)
		})
	}
}

func TestWorkspaceDiffCompilerReviewScopeWorkspace(t *testing.T) {
	for _, backend := range []string{"sqlite", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := newTimeoutReproRuntime(t, backend)
			oracle := &savedWriterOracle{ConsumerRuntime: rt, bytes: map[string][]byte{}}
			workspaceTestWorker(t)
			repo := filepath.Join(t.TempDir(), "repo")
			workspaceRepo(t, repo, "source")
			key := submitGitLifecycle(t, rt, repo, fmt.Sprintf(`id: saved-override
version: "1"
sequence:
 - id: first
   op: workspace_probe
   inputs: {write: first.txt}
 - id: second
   op: workspace_probe
   inputs: {write: second.txt}
 - id: enclosing
   sequence:
    - id: other
      workspace: {cell: test, repo: %q}
      sequence:
       - id: prefix
         op: workspace_probe
         inputs: {write: other-prefix.txt}
       - id: first
         op: workspace_probe
         inputs: {write: other-first.txt}
       - id: inherited
         sequence:
          - id: second
            op: workspace_probe
            inputs: {write: other-second.txt}
 - id: continuation
   op: workspace_probe
   const: true
`, celltest.Repo(repo)))
			out, err := runGitLifecycle(t, oracle, key)
			require.NoError(t, err)
			gitLifecycleOutput(t, out)
			api := &jobinspect.Service{Runtime: rt, Tenant: key.TenantId}
			latest := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId})
			require.Empty(t, latest.Workspace.ScopeID)
			story, err := api.Inspect(ctx, jobinspect.Request{Operation: "story", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var inherited string
			for _, sc := range story.Scopes {
				if strings.HasSuffix(sc.ID, `scope:"sequence:inherited"#1`) {
					inherited = sc.ID
				}
			}
			require.NotEmpty(t, inherited)
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: key.JobId, Limit: 100})
			require.NoError(t, err)
			var second string
			for _, task := range log.Tasks {
				if strings.Contains(task.Path, "/inherited/") && strings.HasSuffix(task.Path, "/second") {
					second = task.ID
				}
			}
			require.NotEmpty(t, second)
			for _, boundary := range []*string{nil, savedPointer("before"), savedPointer("after")} {
				options := jobinspect.WorkspaceDiffOptions{Scope: &inherited, Boundary: boundary}
				selected := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: options})
				require.NotEmpty(t, selected.Workspace.ScopeID)
				// A native task at the same boundary supplies an independent endpoint oracle.
				options.Scope, options.Task = nil, &second
				want := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: options})
				require.Equal(t, want.Workspace, selected.Workspace)
				require.Equal(t, want.Source, selected.Source)
				options.Scope, options.Task, options.Workspace = &inherited, nil, savedPointer("main")
				main, body, err := api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: options})
				require.NoError(t, err)
				if body != nil {
					require.NoError(t, body.Close())
				}
				require.NotEqual(t, selected.Workspace, main.Workspace)
			}
			var scope string
			for _, sc := range story.Scopes {
				if strings.HasSuffix(sc.ID, `scope:"sequence:enclosing"#1`) {
					scope = sc.ID
				}
			}
			require.NotEmpty(t, scope)
			for _, boundary := range []string{"before", "after"} {
				m := savedCompilerRead(t, ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: key.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Scope: &scope, Boundary: &boundary}})
				require.Empty(t, m.Workspace.ScopeID)
				require.Equal(t, latest.Source, m.Source)
			}
		})
	}
}

func TestWorkspaceDiffCompilerS11S21F03ParallelChildren(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			workspaceTestWorker(t)
			f := newGroupingFixture(t, backend, `id: parent
version: "1"
perms: {jobdb: {jobs: {submit: true}}}
sequence:
 - id: first
   op: workspace_probe
   inputs: {write: parent-first}
 - id: second
   op: workspace_probe
   inputs: {write: parent-second}
 - id: children
   child_group:
    mode: run_and_get_result
    children:
     - {key: a, recipe: a, cell_name: test, required: true}
     - {key: b, recipe: b, cell_name: test, required: true}
 - id: continuation
   op: workspace_probe
   const: true
`)
			oracle := &savedWriterOracle{ConsumerRuntime: f.rt, bytes: map[string][]byte{}}
			guard := executionruntime.New(oracle, execution.Allocation{SchemaVersion: 1})
			guard.Issuer = f.guard.(*executionruntime.Runtime).Issuer
			f.guard = guard
			f.installChildren(t, map[string]string{
				"a": "id: a\nsequence:\n - id: first\n   op: workspace_probe\n   inputs: {write: child-a-first}\n - id: second\n   op: workspace_probe\n   inputs: {write: child-a-second}\n",
				"b": "id: b\nsequence:\n - id: first\n   op: workspace_probe\n   inputs: {write: child-b-first}\n - id: second\n   op: workspace_probe\n   inputs: {write: child-b-second}\n",
			})
			f.start.JobContext.Workflow.CellName = "test"
			parent := f.startParent(t)
			initial := f.run(t, parent.Key(), nil)

			require.Equal(t, jobworkflow.JobRunSuspended, initial.Status, "%v", initial.JobError)
			api := &jobinspect.Service{Runtime: f.rt, Tenant: parent.TenantID}
			log, err := api.Inspect(f.ctx, jobinspect.Request{Operation: "log", JobID: parent.JobID, Limit: 100})
			require.NoError(t, err)
			var second string
			for _, task := range log.Tasks {
				if strings.HasSuffix(task.Path, "/second") {
					second = task.ID
				}
			}
			require.NotEmpty(t, second)
			q := jobinspect.WorkspaceDiffRequest{JobID: parent.JobID, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &second}}
			pinned := savedCompilerRead(t, f.ctx, api, oracle, q)
			jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{parent.TenantID}, ParentJobIDs: []string{parent.JobID}})
			require.NoError(t, err)
			require.Len(t, jobs.Jobs, 2)
			// Finish the second submitted child first, with parent reads between results.
			for i := len(jobs.Jobs) - 1; i >= 0; i-- {
				child := jobs.Jobs[i].JobKey
				out := f.run(t, child, nil)
				require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
				require.Nil(t, out.JobError)
				got := savedCompilerRead(t, f.ctx, api, oracle, q)
				require.Equal(t, pinned.Workspace, got.Workspace)
				require.Equal(t, pinned.Source, got.Source)
			}
			out := f.run(t, parent.Key(), nil)
			for i := 0; out.Status == jobworkflow.JobRunSuspended && i < 8; i++ {
				out = f.run(t, parent.Key(), nil)
			}
			require.Equal(t, jobworkflow.JobRunCompleted, out.Status, "%v", out.JobError)
			require.Nil(t, out.JobError)
			inventories := map[string]json.RawMessage{}
			for _, key := range []jobdb.JobKey{parent.Key(), jobs.Jobs[0].JobKey, jobs.Jobs[1].JobKey} {
				inventories[key.JobId], err = changestest.Inventory(f.ctx, f.rt, key)
				require.NoError(t, err)
			}
			latest := savedCompilerRead(t, f.ctx, api, oracle, jobinspect.WorkspaceDiffRequest{JobID: parent.JobID})
			require.Equal(t, pinned.Workspace, latest.Workspace, "joining children preserves the caller's accepted workspace")
			require.Equal(t, pinned.Source, latest.Source)
			require.Equal(t, parent.JobID, latest.Source.Producer.JobID)
			for _, key := range []jobdb.JobKey{parent.Key(), jobs.Jobs[0].JobKey, jobs.Jobs[1].JobKey} {
				after, err := changestest.Inventory(f.ctx, f.rt, key)
				require.NoError(t, err)
				require.Equal(t, inventories[key.JobId], json.RawMessage(after))
			}
		})
	}
}
