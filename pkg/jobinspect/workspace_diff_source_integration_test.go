package jobinspect_test

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceDiffNativeS02S06S08S09S22(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			workspacedifftest.PrepareCLI(t)
			for _, variant := range []string{"missing-patch", "contradictory-base", "unknown-base", "other-workspace", "no-lineage", "parent-only", "fake-parent-proof", "arbitrary-diff", "conflicting-reference", "base-reset", "future-source"} {
				t.Run(variant, func(t *testing.T) {
					ctx, rt := savedBackend(t, backend)
					f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Transform: func(steps []workspacedifftest.Step) []workspacedifftest.Step {
						switch variant {
						case "parent-only":
							steps[2].ParentOnly = true
						case "fake-parent-proof":
							steps[2].ParentOnly = true
							steps[2].Output = map[string]any{"commit_hash": steps[2].After.HeadCommit, "parent_hash": steps[2].After.BaseCommit}
						case "arbitrary-diff":
							steps[2].Omit = true
							steps[2].Arbitrary = true
						case "conflicting-reference":
							steps[2].Refs = map[string]artifacts.Ref{"diff_from_base.diff": artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "not-the-original-producer", TaskOrdinal: 1, Name: "diff_from_base.diff", SizeBytes: 1})}
						case "base-reset":
							steps[2].After.EmptyBase = false
							steps[2].After.BaseCommit = steps[1].After.HeadCommit
						case "future-source":
							steps[2].Omit = true
							steps[3].Base = steps[2].Base
						case "missing-patch":
							steps[2].Omit = true
						case "contradictory-base":
							steps[2].After.BaseCommit = steps[1].After.HeadCommit
						case "unknown-base":
							steps[2].After.EmptyBase = false
						case "other-workspace":
							steps[2].Before.ScopeID = "other"
							steps[2].After.ScopeID = "other"
						case "no-lineage":
							steps[3].Before.HeadCommit = steps[0].After.HeadCommit
						}
						return steps
					}})
					require.NoError(t, err)
					api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
					log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
					require.NoError(t, err)
					q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: strp(log.Tasks[2].ID)}}
					if variant == "no-lineage" {
						q.Task = strp(log.Tasks[3].ID)
					}
					if variant == "other-workspace" {
						q.Workspace = strp("main")
					}
					for _, reader := range []jobinspect.WorkspaceDiffAPI{api, savedHelper(t, ctx, api, f.Job.JobId), savedCLIReader(t, ctx, api, f.Job.JobId)} {
						m, body, err := reader.OpenWorkspaceDiff(ctx, q)
						require.NoError(t, err)
						require.Equal(t, "unavailable", m.Status)
						require.Nil(t, body)
						require.NotEmpty(t, m.Reasons)
						if variant == "other-workspace" {
							other := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Workspace: strp("other")}}
							m, body, err = reader.OpenWorkspaceDiff(ctx, other)
							require.NoError(t, err)
							require.Equal(t, "available", m.Status)
							require.Equal(t, "other", m.Workspace.ScopeID)
							require.NoError(t, body.Close())
						}
					}
				})
			}
		})
	}
}

type savedFaultRuntime struct {
	jobinspect.Runtime
	kind                 string
	opened, closed, read int
}

func (r *savedFaultRuntime) OpenArtifact(ctx context.Context, q jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	if q.Name != "diff_from_base.diff" {
		return r.Runtime.OpenArtifact(ctx, q)
	}
	r.opened++
	switch r.kind {
	case "missing":
		return nil, fs.ErrNotExist
	case "denied":
		return nil, jobdb.ErrLeasePermissionDenied
	case "storage":
		return nil, errors.New("provider fault")
	}
	a, err := r.Runtime.OpenArtifact(ctx, q)
	if err != nil {
		return nil, err
	}
	return &savedFaultArtifact{ArtifactReader: a, owner: r}, nil
}

type savedFaultArtifact struct {
	jobdb.ArtifactReader
	owner *savedFaultRuntime
}

func (a *savedFaultArtifact) Open() (io.ReadCloser, error) {
	if a.owner.kind == "open" {
		return nil, errors.New("open fault")
	}
	r, err := a.ArtifactReader.Open()
	if err != nil {
		return nil, err
	}
	return &savedFaultBody{ReadCloser: r, owner: a.owner, remaining: a.Size()}, nil
}

type savedFaultBody struct {
	io.ReadCloser
	owner     *savedFaultRuntime
	remaining int64
}

func (r *savedFaultBody) Read(p []byte) (int, error) {
	r.owner.read++
	if r.owner.kind == "read" {
		return 0, errors.New("read fault")
	}
	if r.owner.kind == "short" && r.remaining <= 2 {
		return 0, io.EOF
	}
	if r.owner.kind == "short" && len(p) > 2 {
		p = p[:2]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	if err == io.EOF && r.owner.kind == "extra" {
		r.owner.kind = ""
		return copy(p, []byte("extra")), nil
	}
	return n, err
}
func (r *savedFaultBody) Close() error {
	r.owner.closed++
	err := r.ReadCloser.Close()
	if r.owner.kind == "close" {
		return errors.Join(err, errors.New("close fault"))
	}
	return err
}
func TestWorkspaceDiffNativeI05M04E01StreamFaults(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.Build(ctx, rt, t.TempDir())
			require.NoError(t, err)
			for _, kind := range []string{"missing", "denied", "storage", "open", "read", "short", "extra", "close"} {
				t.Run(kind, func(t *testing.T) {
					wrapper := &savedFaultRuntime{Runtime: rt, kind: kind}
					api := &jobinspect.Service{Runtime: wrapper, Tenant: f.Job.TenantId}
					m, body, err := api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
					if kind == "missing" {
						require.NoError(t, err)
						require.Equal(t, "unavailable", m.Status)
						require.Nil(t, body)
						return
					}
					if err == nil {
						_, readErr := io.ReadAll(body)
						err = errors.Join(readErr, body.Close())
					}
					var typed *jobinspect.WorkspaceDiffError
					require.ErrorAs(t, err, &typed)
					require.NotEqual(t, "unavailable", typed.Code)
					if kind == "denied" {
						require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
					}
					if kind == "short" || kind == "extra" {
						require.Equal(t, "byte_count", typed.Code)
					}
					if kind == "read" {
						require.Equal(t, "read", typed.Code)
					}
					if kind == "close" {
						require.Equal(t, "close", typed.Code)
						require.Zero(t, wrapper.read, "metadata probe does not consume bytes through the c2j port")
					}
				})
			}
		})
	}
}

func TestWorkspaceDiffNativeS07S08I04A01A02Foreign(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			zero, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{EmptyComparison: true})
			require.NoError(t, err)
			api := &jobinspect.Service{Runtime: rt, Tenant: zero.Job.TenantId}
			m, body, err := api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: zero.Job.JobId})
			require.NoError(t, err)
			require.Equal(t, "empty", m.Status)
			require.NotNil(t, m.Source)
			require.NotNil(t, m.Bytes)
			require.Zero(t, *m.Bytes)
			require.True(t, m.Workspace.EmptyBase)
			require.Empty(t, m.Workspace.BaseCommit)
			data, err := io.ReadAll(body)
			require.NoError(t, err)
			require.Empty(t, data)
			require.NoError(t, body.Close())
			source, err := workspacedifftest.Build(ctx, rt, t.TempDir())
			require.NoError(t, err)
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: source.Job.JobId})
			require.NoError(t, err)
			var incoming *jobdb.ArtifactKey
			for _, art := range log.Tasks[2].Artifacts {
				if art.Name == "__git_state_thin_pack__" {
					key := art.Reference.Stored.Key
					incoming = &key
				}
			}
			require.NotNil(t, incoming)
			state := source.Steps[2].After
			child, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Incoming: incoming, Transform: func([]workspacedifftest.Step) []workspacedifftest.Step {
				return []workspacedifftest.Step{{Before: state, After: state}}
			}})
			require.NoError(t, err)
			authorized := []string{}
			api.Authorize = func(_ context.Context, id string) error { authorized = append(authorized, id); return nil }
			m, body, err = api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: child.Job.JobId})
			require.NoError(t, err)
			require.Equal(t, "available", m.Status, "%+v", m.Reasons)
			require.Equal(t, child.Job.JobId, m.StateProducer.JobID)
			require.Equal(t, source.Job.JobId, m.Source.Producer.JobID)
			data, err = io.ReadAll(body)
			require.NoError(t, err)
			require.Equal(t, source.Steps[2].Base, data)
			require.NoError(t, body.Close())
			require.Contains(t, authorized, source.Job.JobId)
			api.Authorize = func(_ context.Context, id string) error {
				if id == source.Job.JobId {
					return jobdb.ErrLeasePermissionDenied
				}
				return nil
			}
			m, body, err = api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: child.Job.JobId})
			require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
			require.Nil(t, m)
			require.Nil(t, body)
		})
	}
}

func TestWorkspaceDiffNativeM02UnknownSizes(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{UnknownSizes: true})
			require.NoError(t, err)
			api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			m, body, err := api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
			require.NoError(t, err)
			require.Equal(t, "available", m.Status, "%v", m.Reasons)
			require.EqualValues(t, len(f.Steps[2].Base), m.Source.Key.SizeBytes, "the backend resolves writer-advertised unknown size during native persistence")
			require.NotNil(t, m.Bytes)
			if m.Bytes != nil {
				require.EqualValues(t, len(f.Steps[2].Base), *m.Bytes, "only known exact metadata may supply a count")
			}
			got, err := io.ReadAll(body)
			require.NoError(t, err)
			require.NoError(t, body.Close())
			require.Equal(t, f.Steps[2].Base, got)
			require.NoError(t, jobinspect.ValidateWorkspaceDiff(m.Requested, m))
		})
	}
}

func TestWorkspaceDiffNativeS04P01P04P05OrdinaryProof(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		workspacedifftest.PrepareCLI(t)
		for _, variant := range []string{"ordinary", "missing-cumulative", "arbitrary-op", "version", "wrong-input-root"} {
			t.Run(backend+"/"+variant, func(t *testing.T) {
				ctx, rt := savedBackend(t, backend)
				opt := workspacedifftest.OrdinaryOptions{}
				switch variant {
				case "missing-cumulative":
					opt.OmitBase = true
				case "arbitrary-op":
					opt.CommandOperation = "arbitrary"
				case "version":
					opt.Contract = 2
				case "wrong-input-root":
					opt.WrongRoot = true
				}
				f, err := workspacedifftest.BuildOrdinary(ctx, rt, t.TempDir(), opt)
				require.NoError(t, err)
				api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
				log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
				require.NoError(t, err)
				require.Len(t, log.Tasks, 2)
				for _, reader := range []jobinspect.WorkspaceDiffAPI{api, savedHelper(t, ctx, api, f.Job.JobId), savedCLIReader(t, ctx, api, f.Job.JobId)} {
					for i, task := range log.Tasks {
						m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &task.ID}})
						require.NoError(t, err)
						available := variant == "ordinary" || variant == "missing-cumulative" && i == 0 || (variant == "arbitrary-op" || variant == "wrong-input-root") && i == 1
						if !available {
							require.Equal(t, "unavailable", m.Status)
							require.Nil(t, body)
							continue
						}
						require.Equal(t, "available", m.Status, "%v", m.Reasons)
						got, err := io.ReadAll(body)
						require.NoError(t, err)
						require.NoError(t, body.Close())
						want := f.Steps[i].Parent
						if i == 1 {
							want = f.Steps[i].Base
							require.NotEqual(t, f.Steps[i].Parent, want)
						}
						require.Equal(t, want, got)
						if i == 0 {
							require.Equal(t, "diff_from_parent.diff", m.Source.Key.Name)
						} else {
							require.Equal(t, "diff_from_base.diff", m.Source.Key.Name)
						}
					}
				}
			})
		}
	}
}

func TestWorkspaceDiffNativeS09I08ConflictingLineage(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			workspacedifftest.PrepareCLI(t)
			for _, foreign := range []bool{false, true} {
				name := "unchanged"
				if foreign {
					name = "foreign"
				}
				t.Run(name, func(t *testing.T) {
					ctx, rt := savedBackend(t, backend)
					source, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Transform: func(steps []workspacedifftest.Step) []workspacedifftest.Step {
						// A physically persisted unchanged observer with contradictory patch
						// provenance must not be skipped to reuse the preceding valid source.
						steps[3].Base, steps[3].Bundle = steps[2].Base, steps[2].Bundle
						steps[3].Refs = map[string]artifacts.Ref{"diff_from_base.diff": artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "substitution", TaskOrdinal: 1, Name: "diff_from_base.diff", SizeBytes: 1})}
						return append(steps, workspacedifftest.Step{Before: steps[3].After, After: steps[3].After})
					}})
					require.NoError(t, err)
					api := &jobinspect.Service{Runtime: rt, Tenant: source.Job.TenantId}
					selected := source
					if foreign {
						log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: source.Job.JobId})
						require.NoError(t, err)
						var incoming *jobdb.ArtifactKey
						for _, a := range log.Tasks[3].Artifacts {
							if a.Name == "__git_state_thin_pack__" {
								k := a.Reference.Stored.Key
								incoming = &k
							}
						}
						require.NotNil(t, incoming)
						state := source.Steps[3].After
						selected, err = workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Incoming: incoming, Transform: func([]workspacedifftest.Step) []workspacedifftest.Step {
							return []workspacedifftest.Step{{Before: state, After: state}}
						}})
						require.NoError(t, err)
					}
					for _, reader := range []jobinspect.WorkspaceDiffAPI{api, savedHelper(t, ctx, api, selected.Job.JobId), savedCLIReader(t, ctx, api, selected.Job.JobId)} {
						m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: selected.Job.JobId})
						require.NoError(t, err)
						require.Equal(t, "unavailable", m.Status)
						require.Contains(t, m.Reasons[0], "conflicting")
						require.Nil(t, body)
					}
				})
			}
		})
	}
}

func TestWorkspaceDiffNativeA01ReauthorizeAtOpen(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			workspacedifftest.PrepareCLI(t)
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.Build(ctx, rt, t.TempDir())
			require.NoError(t, err)
			for _, surface := range []string{"Service", "Helper", "CLI"} {
				t.Run(surface, func(t *testing.T) {
					wrapper := &savedFaultRuntime{Runtime: rt}
					service := &jobinspect.Service{Runtime: wrapper, Tenant: f.Job.TenantId, Authorize: func(context.Context, string) error {
						if wrapper.opened != 0 {
							return jobdb.ErrLeasePermissionDenied
						}
						return nil
					}}
					var reader jobinspect.WorkspaceDiffAPI = service
					if surface == "Helper" {
						reader = savedHelper(t, ctx, service, f.Job.JobId)
					}
					if surface == "CLI" {
						reader = savedCLIReader(t, ctx, service, f.Job.JobId)
					}
					m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
					if surface == "CLI" {
						require.ErrorContains(t, err, "denied")
					} else {
						var typed *jobinspect.WorkspaceDiffError
						require.ErrorAs(t, err, &typed)
						require.Equal(t, "denied", typed.Code)
					}
					require.Nil(t, m)
					require.Nil(t, body)
					require.Equal(t, 1, wrapper.opened)
					require.Equal(t, 1, wrapper.closed)
					require.Zero(t, wrapper.read)
				})
			}
		})
	}
}
