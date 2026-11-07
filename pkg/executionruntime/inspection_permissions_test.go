package executionruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/colony-2/c2j/pkg/starter"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// INS-002–007, INS-014–016, INS-019: real persisted lineage, outputs,
// artifacts and Git snapshots on both backend transports, without submit grants.
func TestTaskInspectionUsesBackendReadPermissions(t *testing.T) {
	for _, backend := range []string{"toy", "remote"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			var base jobdb.WorkflowRuntime = toy.New()
			if backend == "remote" {
				server := httptest.NewServer(celltest.Handler(remote.NewServer(base)))
				defer server.Close()
				client, err := remote.New(server.URL, server.Client())
				require.NoError(t, err)
				base = client
			}
			policy := &inspectionPolicyRuntime{WorkflowRuntime: base}
			rt := New(policy, execution.Allocation{})
			celltest.Create(t, rt, "tenant", "cell", "example.test/cell")
			celltest.Create(t, rt, "tenant", "other-cell", "example.test/other-cell")
			types := map[jobdb.JobKey]string{}
			submit := func(parent jobdb.ExecutionLease, cell, typ string) jobdb.JobHandle {
				req := jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", Cell: cell, CellRepo: "example.test/" + cell, JobType: typ, Data: jobdb.NewTaskDataOrPanic(nil)}}
				var h jobdb.JobHandle
				var err error
				if parent == nil {
					h, err = rt.SubmitJob(ctx, req)
				} else {
					h, err = parent.SubmitJob(ctx, req)
				}
				require.NoError(t, err)
				types[h.JobKey] = typ
				return h
			}
			claim := func(h jobdb.JobHandle) jobdb.ExecutionLease {
				l, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: h.JobKey, Routes: []jobdb.Route{{JobType: types[h.JobKey]}}, WorkerID: "fixture", LeaseDuration: time.Minute})
				require.NoError(t, err)
				return l
			}
			ancestor := submit(nil, "cell", "ancestor")
			ancestorLease := claim(ancestor)
			parent := submit(ancestorLease, "cell", "parent")
			parentLease := claim(parent)
			self := submit(parentLease, "cell", "self")
			sibling := submit(parentLease, "cell", "sibling")
			selfLease := claim(self)
			child := submit(selfLease, "cell", "child")
			childLease := claim(child)
			descendant := submit(childLease, "cell", "descendant")
			unrelated := submit(nil, "other-cell", "unrelated")
			require.NotEqual(t, self.GoalID, unrelated.GoalID)
			meta, err := json.Marshal(starter.JobMetadata{ParentTenantID: "tenant", ParentJobID: self.JobKey.JobId})
			require.NoError(t, err)
			pretender, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", Cell: "cell", CellRepo: "example.test/cell", JobType: "pretender", Metadata: meta, Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)

			// Local bundle has the exact saved commit and requires no remote origin.
			repo := t.TempDir()
			git := func(args ...string) string {
				c := exec.Command("git", args...)
				c.Dir = repo
				c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
				b, e := c.CombinedOutput()
				require.NoError(t, e, string(b))
				return strings.TrimSpace(string(b))
			}
			git("init", "--quiet")
			require.NoError(t, os.WriteFile(filepath.Join(repo, "saved.txt"), []byte("base\n"), 0600))
			git("add", ".")
			git("commit", "-qm", "base")
			baseCommit := git("rev-parse", "HEAD")
			require.NoError(t, os.WriteFile(filepath.Join(repo, "saved.txt"), []byte("saved\n"), 0600))
			git("add", ".")
			git("commit", "-qm", "saved")
			head := git("rev-parse", "HEAD")
			bundle := filepath.Join(t.TempDir(), "saved.bundle")
			git("bundle", "create", bundle, "HEAD")
			bundleBytes, err := os.ReadFile(bundle)
			require.NoError(t, err)
			envelope, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"output": map[string]any{"answer": 42}, "workspace_after": coretask.WorkspaceState{Repository: repo, Cell: "other-cell", ScopeID: "saved", BaseCommit: baseCommit, HeadCommit: head}})
			require.NoError(t, err)
			raw, err := json.Marshal(envelope)
			require.NoError(t, err)
			task := delegationTask(func(context.Context, wf.TaskContext, jobdb.TaskData) (jobdb.TaskData, error) {
				return &jobdb.SimpleTaskData{Data: raw, Artifacts: []jobdb.Artifact{jobdb.NewArtifactFromBytes("check.bin", []byte{0, 255, 10}), jobdb.NewArtifactFromBytes(gitstate.ThinPackArtifactName, bundleBytes)}}, nil
			})
			worker := wf.ExecutionWorker{JobType: "unrelated", Execute: func(c wf.ExecutionContext, _ jobdb.JobData) (jobdb.JobData, error) {
				_, e := c.DoTask(wf.TaskCall{Site: "saved", TaskType: task.Name(), ContractVersion: 1, Command: json.RawMessage(`{}`)}, func(context.Context) (jobdb.TaskData, error) { return jobdb.NewTaskDataOrPanic(nil), nil })
				return jobdb.NewTaskDataOrPanic(map[string]any{"answer": 42}), e
			}}
			run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: unrelated.JobKey, JobWorker: worker, TaskWorkers: []wf.TaskWorker{task}, WorkerID: "fixture", LeaseDuration: time.Minute})
			require.NoError(t, err)
			out, err := run.Run(nil)
			require.NoError(t, err)
			require.Equal(t, wf.JobRunCompleted, out.Status)
			owner := rt.leases[self.JobKey]
			_, scope, cancel, err := rt.restrictedAccess(ctx, wf.TaskContext{JobKey: self.JobKey}, owner, permissions.JobDB{}, nil)
			require.NoError(t, err)
			defer cancel()
			api := jobinspect.FromContext(scope)
			for name, h := range map[string]jobdb.JobHandle{"ancestor": ancestor, "parent": parent, "self": self, "sibling": sibling, "child": child, "descendant": descendant, "unrelated": unrelated, "pretender": pretender} {
				t.Run(name, func(t *testing.T) {
					r, e := api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: h.JobKey.JobId})
					require.NoError(t, e)
					require.Equal(t, h.JobKey.JobId, r.Job.ID)
				})
			}
			server, err := childbroker.Start(scope, childbroker.Options{Current: jobcontext.Current{TenantID: "tenant", JobID: self.JobKey.JobId, GoalID: self.GoalID}, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
			require.NoError(t, err)
			defer server.Close()
			broker, _, err := jobcontext.ChildJobBrokerFromEnv(func(k string) string { return server.Env()[k] })
			require.NoError(t, err)
			client := childbroker.JobClient{Broker: broker}
			siblingResponse, err := client.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: sibling.JobKey.JobId})
			require.NoError(t, err)
			require.Equal(t, sibling.JobKey.JobId, siblingResponse.Job.ID)

			standalone := &jobinspect.Service{Runtime: base, Tenant: "tenant"}
			for _, q := range []jobinspect.Request{{Operation: "list", Limit: 2}, {Operation: "list", Parent: parent.JobKey.JobId, Limit: 1}, {Operation: "list", Parent: ancestor.JobKey.JobId}, {Operation: "list", Status: []jobdb.JobStatus{jobdb.JobStatusCompleted}}} {
				for {
					want, e := standalone.Inspect(ctx, q)
					require.NoError(t, e)
					got, e := api.Inspect(ctx, q)
					require.NoError(t, e)
					require.Equal(t, want, got)
					viaHelper, e := client.Inspect(ctx, q)
					require.NoError(t, e)
					require.Equal(t, want, viaHelper)
					if want.NextCursor == "" {
						break
					}
					q.Cursor = want.NextCursor
				}
			}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: unrelated.JobKey.JobId})
			require.NoError(t, err)
			require.Len(t, log.Tasks, 1)
			taskID := log.Tasks[0].ID
			for _, reader := range []jobinspect.API{api, client} {
				pointer := "/answer"
				exact, e := reader.Inspect(ctx, jobinspect.Request{Operation: "result", JobID: unrelated.JobKey.JobId, Field: &pointer})
				require.NoError(t, e)
				require.Equal(t, "42", string(exact.Value))

				for _, op := range []string{"show", "log", "story", "result", "fields", "artifacts", "snapshot"} {
					r, e := reader.Inspect(ctx, jobinspect.Request{Operation: op, JobID: unrelated.JobKey.JobId, Task: taskID})
					require.NoError(t, e, op)
					require.Equal(t, jobinspect.Version, r.Version)
					switch op {
					case "log":
						require.NotEmpty(t, r.Task)
					case "story":
						require.NotEmpty(t, r.Scopes)
					case "result":
						require.NotEmpty(t, r.Result)
					case "fields":
						require.NotEmpty(t, r.Fields)
					case "artifacts":
						require.NotEmpty(t, r.Artifacts)
					case "snapshot":
						require.Equal(t, "available", r.Snapshot.Workspaces[0].Availability)
					}

				}
				r, e := reader.Download(ctx, jobinspect.Request{JobID: unrelated.JobKey.JobId, Task: taskID, Name: "check.bin"})
				require.NoError(t, e)
				b, e := io.ReadAll(r)
				require.NoError(t, e)
				require.NoError(t, r.Close())
				require.Equal(t, []byte{0, 255, 10}, b)
				dir := filepath.Join(t.TempDir(), "export")
				snap, e := jobinspect.Export(ctx, reader, jobinspect.Request{JobID: unrelated.JobKey.JobId, Task: taskID}, dir)
				require.NoError(t, e)
				require.Equal(t, head, snap.Workspaces[0].HeadCommit)
				require.Equal(t, head, git("-C", filepath.Join(dir, snap.Workspaces[0].Directory), "rev-parse", "HEAD"))

				b, e = os.ReadFile(filepath.Join(dir, snap.Workspaces[0].Directory, "saved.txt"))
				require.NoError(t, e)
				require.Equal(t, "saved\n", string(b))
				require.Len(t, snap.Artifacts, 1)
				b, e = os.ReadFile(filepath.Join(dir, snap.Artifacts[0].Path))
				require.NoError(t, e)
				require.Equal(t, []byte{0, 255, 10}, b)
			}
			inventory, err := api.Inspect(ctx, jobinspect.Request{Operation: "artifacts", JobID: unrelated.JobKey.JobId, Task: taskID})
			require.NoError(t, err)
			var key jobdb.ArtifactKey
			for _, a := range inventory.Artifacts {
				if a.Name == "check.bin" {
					key = a.Reference.Stored.Key
				}
			}
			require.NotEmpty(t, key.JobId)
			var externalCalls atomic.Int32
			externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { externalCalls.Add(1); fmt.Fprint(w, "external") }))
			defer externalServer.Close()
			refJob := submit(nil, "cell", starter.RecipeJobType)
			refWorker := wf.ExecutionWorker{JobType: starter.RecipeJobType, Execute: func(wf.ExecutionContext, jobdb.JobData) (jobdb.JobData, error) {
				return jobdb.NewTaskDataOrPanic(map[string]any{"artifact_refs": map[string]artifacts.Ref{"forwarded": artifacts.NewStoredRef(key), "external": artifacts.NewExternalRef("external", externalServer.URL, false)}}), nil
			}}
			refRun, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: refJob.JobKey, JobWorker: refWorker, WorkerID: "fixture", LeaseDuration: time.Minute})
			require.NoError(t, err)
			_, err = refRun.Run(nil)
			require.NoError(t, err)
			for _, reader := range []jobinspect.API{api, client} {
				r, e := reader.Download(ctx, jobinspect.Request{JobID: refJob.JobKey.JobId, Name: "forwarded"})
				require.NoError(t, e)
				b, e := io.ReadAll(r)
				require.NoError(t, e)
				require.NoError(t, r.Close())
				require.Equal(t, []byte{0, 255, 10}, b)
			}
			for _, reader := range []jobinspect.API{api, client} {
				_, err = reader.Inspect(ctx, jobinspect.Request{Operation: "artifacts", JobID: refJob.JobKey.JobId})
				require.NoError(t, err)
				_, err = reader.Download(ctx, jobinspect.Request{JobID: refJob.JobKey.JobId, Name: "external"})
				require.ErrorContains(t, err, "external")
			}
			require.Zero(t, externalCalls.Load())
			// INS-013, INS-027, INS-030: backend authority and tenant selection survive.
			for _, mode := range []string{"list", "detail", "artifact"} {
				policy.setDenied(mode)
				for _, reader := range []jobinspect.API{api, client} {
					switch mode {
					case "list":
						_, err = reader.Inspect(ctx, jobinspect.Request{Operation: "list"})
					case "detail":
						_, err = reader.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: unrelated.JobKey.JobId})
					case "artifact":
						_, err = reader.Download(ctx, jobinspect.Request{JobID: refJob.JobKey.JobId, Name: "forwarded"})
					}
					require.Error(t, err, mode)
					require.Contains(t, err.Error(), "backend denied")
				}
			}
			policy.setDenied("")
			policy.resetLists()
			_, err = api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: unrelated.JobKey.JobId})
			require.NoError(t, err)
			// JobDB's GetJobRun summary plus optional identity enrichment, no ancestry probe.
			lists, tenant := policy.observed()
			require.Equal(t, 2, lists)
			require.Equal(t, "tenant", tenant)
			// Metadata cannot override an independent backend denial either.
			policy.setDenied("detail")
			_, err = api.Inspect(ctx, jobinspect.Request{Operation: "show", JobID: pretender.JobKey.JobId})
			require.Error(t, err)
			policy.setDenied("")

		})
	}
}

// Fakes fail loudly if a guard accidentally reaches the underlying runtime.
type inspectionExpiryLease struct {
	jobdb.ExecutionLease
	expiry time.Time
}

func (l inspectionExpiryLease) LeaseExpiry() time.Time { return l.expiry }

type inspectionReadCounter struct {
	jobdb.WorkflowRuntime
	reads int
}

func (r *inspectionReadCounter) ListJobs(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.reads++
	return jobdb.ListJobsResponse{}, fmt.Errorf("unexpected backend read")
}
func (r *inspectionReadCounter) GetJob(context.Context, jobdb.JobKey) (jobdb.JobInfo, error) {
	r.reads++
	return jobdb.JobInfo{}, fmt.Errorf("unexpected backend read")
}

// INS-010–012: every entry point is guarded, including unfiltered discovery.
func TestTaskInspectionLifetimeGuards(t *testing.T) {
	for _, mode := range []string{"scope", "deadline", "lost", "replaced", "expired", "call"} {
		t.Run(mode, func(t *testing.T) {
			key := jobdb.JobKey{TenantId: "tenant", JobId: "self"}
			counter := &inspectionReadCounter{}
			rt := New(counter, execution.Allocation{})
			owner := &lease{ExecutionLease: inspectionExpiryLease{expiry: time.Now().Add(time.Hour)}, leaseState: &leaseState{}}
			rt.leases[key] = owner
			access, scope, cancel, err := rt.restrictedAccess(context.Background(), wf.TaskContext{JobKey: key}, owner, permissions.JobDB{}, nil)
			require.NoError(t, err)
			defer cancel()
			call := context.Background()
			want := error(jobdb.ErrExecutionLeaseLost)
			switch mode {
			case "scope":
				cancel()
				want = context.Canceled
			case "deadline":
				defer access.LimitDeadline(time.Now().Add(-time.Second))()
				want = context.DeadlineExceeded
			case "lost":
				delete(rt.leases, key)
			case "replaced":
				rt.leases[key] = &lease{leaseState: &leaseState{}}
			case "expired":
				owner.ExecutionLease = inspectionExpiryLease{expiry: time.Now().Add(-time.Second)}
			case "call":
				var stop context.CancelFunc
				call, stop = context.WithCancel(call)
				stop()
				want = context.Canceled
			}
			api := jobinspect.FromContext(scope)
			for _, op := range []string{"show", "list", "download"} {
				var e error
				if op == "download" {
					_, e = api.Download(call, jobinspect.Request{JobID: "unrelated", Name: "x"})
				} else {
					_, e = api.Inspect(call, jobinspect.Request{Operation: op, JobID: "unrelated"})
				}
				require.ErrorIs(t, e, want, op)
			}
			require.Zero(t, counter.reads)
		})
	}
}

// inspectionPolicyRuntime injects backend decisions without changing the task guard.
type inspectionPolicyRuntime struct {
	jobdb.WorkflowRuntime
	mu         sync.Mutex
	denied     string
	lists      int
	lastTenant string
}

func (r *inspectionPolicyRuntime) ListJobs(ctx context.Context, q jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lists++
	if len(q.TenantIds) > 0 {
		r.lastTenant = q.TenantIds[0]
	}
	if r.denied == "list" {
		return jobdb.ListJobsResponse{}, fmt.Errorf("backend denied list")
	}
	return r.WorkflowRuntime.ListJobs(ctx, q)
}
func (r *inspectionPolicyRuntime) GetJob(ctx context.Context, key jobdb.JobKey) (jobdb.JobInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastTenant = key.TenantId
	if r.denied == "detail" {
		return jobdb.JobInfo{}, fmt.Errorf("backend denied detail")
	}
	return r.WorkflowRuntime.GetJob(ctx, key)
}
func (r *inspectionPolicyRuntime) OpenArtifact(ctx context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastTenant = ref.JobKey.TenantId
	if r.denied == "artifact" {
		return nil, fmt.Errorf("backend denied artifact")
	}
	return r.WorkflowRuntime.OpenArtifact(ctx, ref)
}

func (r *inspectionPolicyRuntime) setDenied(mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.denied = mode
}
func (r *inspectionPolicyRuntime) resetLists() { r.mu.Lock(); defer r.mu.Unlock(); r.lists = 0 }
func (r *inspectionPolicyRuntime) observed() (int, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lists, r.lastTenant
}
