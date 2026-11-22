package jobinspect_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func observerSteps(steps []workspacedifftest.Step, omitParent bool) []workspacedifftest.Step {
	steps = append(steps, steps[3])
	for i := 3; i < len(steps); i++ {
		w := steps[i].Before
		g := gitstate.GlobalGitTaskContext{BaseRepo: w.Repository, CellName: "execution-owner", ResolvedBaseHash: w.BaseCommit, PersistHash: w.HeadCommit, ParentHash: w.BaseCommit, Workspace: &contextual.WorkspaceContext{Cell: w.Cell, ScopeID: w.ScopeID, Initial: "empty"}}
		out := contextual.GitCommitContext{PersistHash: w.HeadCommit, ParentHash: w.BaseCommit}
		if omitParent {
			g.ParentHash, out.ParentHash = "", ""
		}
		steps[i].InputGit, steps[i].OutputGit = &g, &out
	}
	return steps
}

type observerReadGuard struct {
	jobinspect.Runtime
	bundles, patches int
}

func (r *observerReadGuard) OpenArtifact(ctx context.Context, q jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	if q.Name == gitstate.ThinPackArtifactName {
		r.bundles++
		return nil, os.ErrPermission
	}
	r.patches++
	return r.Runtime.OpenArtifact(ctx, q)
}

func TestWorkspaceDiffNativePatchlessObservers(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		for _, omit := range []bool{true, false} {
			variant := "omitted-parent"
			if !omit {
				variant = "parent-present"
			}
			t.Run(backend+"/"+variant, func(t *testing.T) {
				ctx, rt := savedBackend(t, backend)
				f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{FixedBase: true, Transform: func(s []workspacedifftest.Step) []workspacedifftest.Step { return observerSteps(s, omit) }})
				require.NoError(t, err)
				require.NotEmpty(t, f.Oracle)
				require.NoDirExists(t, filepath.Join(f.Directory, "repo"))
				require.NoDirExists(t, filepath.Join(f.Directory, "patches"))
				inventory, err := changestest.Inventory(ctx, rt, f.Job)
				require.NoError(t, err)
				readonly := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
				guard := &observerReadGuard{Runtime: readonly}
				api := &jobinspect.Service{Runtime: guard, Tenant: f.Job.TenantId}
				log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
				require.NoError(t, err)
				require.Len(t, log.Tasks, 5)
				var source jobdb.ArtifactKey
				for _, a := range log.Tasks[2].Artifacts {
					if a.Name == "diff_from_base.diff" {
						source = a.Reference.Stored.Key
					}
				}
				require.NotEmpty(t, source.JobId)
				trapDir := t.TempDir()
				marker := filepath.Join(trapDir, "invoked")
				require.NoError(t, os.WriteFile(filepath.Join(trapDir, "git"), []byte("#!/bin/sh\nprintf invoked >> '"+marker+"'\nexit 91\n"), 0700))
				t.Setenv("PATH", trapDir+string(os.PathListSeparator)+os.Getenv("PATH"))
				for _, selection := range []struct {
					name  string
					index int
				}{{"producer", 2}, {"observer-one", 3}, {"observer-two", 4}, {"latest", -1}} {
					t.Run(selection.name, func(t *testing.T) {
						q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId}
						selected := selection.index
						if selected >= 0 {
							q.Task = &log.Tasks[selected].ID
						} else {
							selected = 4
						}
						m, body, err := api.OpenWorkspaceDiff(ctx, q)
						require.NoError(t, err)
						require.Equal(t, "available", m.Status, "recovery acceptance: %v", m.Reasons)
						require.NotNil(t, body)
						got, err := io.ReadAll(body)
						require.NoError(t, err)
						require.NoError(t, body.Close())
						require.Equal(t, f.Oracle, got)
						require.Equal(t, q, m.Requested)
						require.Equal(t, log.Tasks[selected].ID, m.Resolved.Task)
						require.Equal(t, 1, m.Resolved.Attempt)
						require.Equal(t, log.Tasks[selected].ID, m.StateProducer.Task)
						require.Equal(t, f.Job.JobId, m.StateProducer.JobID)
						require.Equal(t, f.Steps[selected].After, *m.Workspace)
						require.NotEmpty(t, m.Cutoff)
						require.Equal(t, log.Tasks[2].ID, m.Source.Producer.Task)
						require.Equal(t, f.Job.JobId, m.Source.Producer.JobID)
						require.Equal(t, source, m.Source.Key)
						again, err := api.Inspect(ctx, jobinspect.Request{Operation: "workspace_diff", JobID: q.JobID, WorkspaceDiff: &q.WorkspaceDiffOptions})
						require.NoError(t, err)
						require.Equal(t, m, again.WorkspaceDiff)
					})
				}
				require.NoFileExists(t, marker, "reader must never execute Git")
				require.Zero(t, guard.bundles)
				require.Zero(t, readonly.Mutations.Load())
				after, err := changestest.Inventory(ctx, rt, f.Job)
				require.NoError(t, err)
				require.Equal(t, inventory, after)
			})
		}
	}
}

func TestWorkspaceDiffNativePatchlessObserverRejections(t *testing.T) {
	variants := []string{"comparison-base", "comparison-cell", "comparison-scope", "comparison-repository", "input-parent", "output-parent", "input-head", "output-head", "resolved-base", "repository", "cell", "scope", "empty", "missing-before", "missing-after", "missing-base", "missing-head", "changed", "missing-link", "conflicting-link", "producer-parent", "reference-only", "malformed-reference", "missing-patch", "source-reference"}
	for _, backend := range []string{"SQLite", "HTTP"} {
		for _, variant := range variants {
			t.Run(backend+"/"+variant, func(t *testing.T) {
				ctx, rt := savedBackend(t, backend)
				opt := workspacedifftest.Options{FixedBase: true}
				if variant == "missing-link" {
					opt.OmitIncoming = map[int]bool{4: true}
				}
				if variant == "conflicting-link" {
					opt.ConflictingIncoming = true
				}
				opt.Transform = func(s []workspacedifftest.Step) []workspacedifftest.Step {
					s = observerSteps(s, true)
					p := &s[4]
					g := p.InputGit
					switch variant {
					case "comparison-base", "comparison-cell", "comparison-scope", "comparison-repository":
						switch variant {
						case "comparison-base":
							p.Before.BaseCommit = strings.Repeat("c", 40)
						case "comparison-cell":
							p.Before.Cell = "other"
						case "comparison-scope":
							p.Before.ScopeID = "other"
						case "comparison-repository":
							p.Before.Repository = "example.test/other"
						}
						p.After = p.Before
						g.BaseRepo, g.Workspace.Cell, g.Workspace.ScopeID = p.Before.Repository, p.Before.Cell, p.Before.ScopeID
						g.ResolvedBaseHash = p.Before.BaseCommit
					case "input-parent":
						g.ParentHash = p.After.HeadCommit
					case "output-parent":
						p.OutputGit.ParentHash = p.After.HeadCommit
					case "input-head":
						g.PersistHash = p.After.BaseCommit
					case "output-head":
						p.OutputGit.PersistHash = p.After.BaseCommit
					case "resolved-base":
						g.ResolvedBaseHash = p.After.HeadCommit
					case "repository":
						g.BaseRepo = "example.test/other"
					case "cell":
						g.Workspace.Cell = "other"
					case "scope":
						g.Workspace.ScopeID = "other"
					case "empty":
						p.Before.EmptyBase = true
						p.After.EmptyBase = true
					case "missing-before":
						p.OmitBefore = true
					case "missing-after":
						p.OmitAfter = true
					case "missing-base":
						g.ResolvedBaseHash = ""
					case "missing-head":
						g.PersistHash = ""
					case "changed":
						p.Before.HeadCommit = p.Before.BaseCommit
					case "producer-parent":
						s[2].OutputGit = &contextual.GitCommitContext{PersistHash: s[2].After.HeadCommit}
					case "reference-only":
						p.Refs = map[string]artifacts.Ref{"diff_from_base.diff": artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "original", TaskOrdinal: 7, Name: "diff_from_base.diff", SizeBytes: 10})}
					case "malformed-reference":
						p.Refs = map[string]artifacts.Ref{"diff_from_parent.diff": {}}
					case "missing-patch":
						s[2].Omit = true
					case "source-reference":
						s[2].Refs = map[string]artifacts.Ref{"diff_from_base.diff": artifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "forged", TaskOrdinal: 7, Name: "diff_from_base.diff", SizeBytes: 10})}
					}
					return s
				}
				f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), opt)
				require.NoError(t, err)
				readonly := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
				guard := &observerReadGuard{Runtime: readonly}
				api := &jobinspect.Service{Runtime: guard, Tenant: f.Job.TenantId}
				log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
				require.NoError(t, err)
				for _, explicit := range []bool{false, true} {
					// Latest main excludes absent after-states and other workspaces.
					// Exercise those tasks through their explicit selectors.
					if (variant == "missing-after" || variant == "comparison-scope" || variant == "comparison-repository") && !explicit {
						continue
					}
					q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId}
					if explicit {
						q.Task = &log.Tasks[4].ID
					}
					m, body, err := api.OpenWorkspaceDiff(ctx, q)
					require.NoError(t, err)
					require.Equal(t, "unavailable", m.Status, "%v", m.Reasons)
					require.Nil(t, body)
					require.Nil(t, m.Source)
				}
				require.Zero(t, guard.patches, "rejected evidence must not open candidate bytes")
				require.Zero(t, guard.bundles)
				require.Zero(t, readonly.Mutations.Load())
			})
		}
	}
}

type observerMembershipRuntime struct{ jobinspect.Runtime }

func (r observerMembershipRuntime) GetChapter(ctx context.Context, q jobdb.ChapterRef) (jobdb.Chapter, error) {
	c, err := r.Runtime.GetChapter(ctx, q)
	filtered := c.Artifacts[:0]
	for _, a := range c.Artifacts {
		if a.Name != "diff_from_base.diff" {
			filtered = append(filtered, a)
		}
	}
	c.Artifacts = filtered
	return c, err
}

func TestWorkspaceDiffNativePatchlessObserverSources(t *testing.T) {
	for _, backend := range []string{"SQLite", "HTTP"} {
		t.Run(backend, func(t *testing.T) {
			workspacedifftest.PrepareCLI(t)
			ctx, rt := savedBackend(t, backend)
			f, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{FixedBase: true, Transform: func(s []workspacedifftest.Step) []workspacedifftest.Step { return observerSteps(s, true) }})
			require.NoError(t, err)
			for _, kind := range []string{"available", "membership", "missing", "denied", "revoked"} {
				t.Run(kind, func(t *testing.T) {
					for _, surface := range []string{"Service", "Helper", "CLI"} {
						t.Run(surface, func(t *testing.T) {
							faults := &savedFaultRuntime{Runtime: rt}
							if kind == "missing" || kind == "denied" {
								faults.kind = kind
							}
							var runtime jobinspect.Runtime = faults
							if kind == "membership" {
								runtime = observerMembershipRuntime{Runtime: runtime}
							}
							api := &jobinspect.Service{Runtime: runtime, Tenant: f.Job.TenantId}
							if kind == "revoked" {
								api.Authorize = func(context.Context, string) error {
									if faults.opened > 0 {
										return jobdb.ErrLeasePermissionDenied
									}
									return nil
								}
							}
							var reader jobinspect.WorkspaceDiffAPI = api
							if surface == "Helper" {
								reader = savedHelper(t, ctx, api, f.Job.JobId)
							}
							if surface == "CLI" {
								reader = savedCLIReader(t, ctx, api, f.Job.JobId)
							}
							m, body, err := reader.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId})
							switch kind {
							case "denied", "revoked":
								require.Error(t, err)
								require.Contains(t, err.Error(), "denied")
								require.Nil(t, body)
								require.Zero(t, faults.read)
							case "missing", "membership":
								require.NoError(t, err)
								require.Equal(t, "unavailable", m.Status)
								require.Nil(t, body)
								if kind == "membership" {
									require.Zero(t, faults.opened)
								}
							default:
								require.NoError(t, err)
								require.Equal(t, "available", m.Status, "%v", m.Reasons)
								data, err := io.ReadAll(body)
								require.NoError(t, err)
								require.NoError(t, body.Close())
								require.Equal(t, f.Oracle, data)
							}
						})
					}
				})
			}
			// A foreign observer retains selected ownership while inspecting and opening
			// the original job under its own authorization check.
			api := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
			log, err := api.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
			require.NoError(t, err)
			var incoming *jobdb.ArtifactKey
			for _, a := range log.Tasks[2].Artifacts {
				if a.Name == gitstate.ThinPackArtifactName {
					k := a.Reference.Stored.Key
					incoming = &k
				}
			}
			require.NotNil(t, incoming)
			child, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Incoming: incoming, Transform: func(s []workspacedifftest.Step) []workspacedifftest.Step {
				s[3].Before, s[3].After = f.Steps[2].After, f.Steps[2].After
				s = observerSteps(s, true)
				return s[3:]
			}})
			require.NoError(t, err)
			m, body, err := api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: child.Job.JobId})
			require.NoError(t, err)
			require.Equal(t, "available", m.Status, "%v", m.Reasons)
			require.Equal(t, child.Job.JobId, m.StateProducer.JobID)
			require.Equal(t, f.Job.JobId, m.Source.Producer.JobID)
			data, err := io.ReadAll(body)
			require.NoError(t, err)
			require.NoError(t, body.Close())
			require.Equal(t, f.Oracle, data)
			api.Authorize = func(_ context.Context, id string) error {
				if id == f.Job.JobId {
					return jobdb.ErrLeasePermissionDenied
				}
				return nil
			}
			_, body, err = api.OpenWorkspaceDiff(ctx, jobinspect.WorkspaceDiffRequest{JobID: child.Job.JobId})
			require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
			require.Nil(t, body)
		})
	}
}
