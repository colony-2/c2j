// Package workspacedifftest supplies native persisted jobs and saved-byte
// oracles to the reader, authenticated transport and CLI acceptance tests.
package workspacedifftest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

type Step struct {
	Before, After        coretask.WorkspaceState
	Parent, Base, Bundle []byte
	Omit                 bool
	ParentOnly           bool
	Arbitrary            bool
	Refs                 map[string]artifacts.Ref
	Output               map[string]any
}
type Fixture struct {
	Job       jobdb.JobKey
	Steps     []Step
	Directory string
}

// Build uses the real patch writer, then a native execution worker to retain
// exact files and fixed incoming snapshot keys. No reader-generated diff is
// used as an oracle. Compiler tests separately exercise actual recipe flow.
type Options struct {
	Transform           func([]Step) []Step
	BeforeTask          func(int) error
	Incoming            *jobdb.ArtifactKey
	Scope               bool
	EmptyComparison     bool
	UnknownSizes        bool
	RootAmbiguous       bool
	SplitScopes         bool
	OmitIncoming        map[int]bool
	RetryTask           *int
	ConflictingIncoming bool
}

func Build(ctx context.Context, rt jobdb.ConsumerRuntime, dir string) (*Fixture, error) {
	return BuildWithOptions(ctx, rt, dir, Options{})
}
func BuildWithOptions(ctx context.Context, rt jobdb.ConsumerRuntime, dir string, opt Options) (*Fixture, error) {
	f := &Fixture{Directory: dir}
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Saved fixture"}, {"config", "user.email", "fixture@example.test"}} {
		if _, err := changestest.Git(ctx, repo, args...); err != nil {
			return nil, err
		}
	}
	before := coretask.WorkspaceState{Repository: "example.test/saved", Cell: "saved", ScopeID: "root", EmptyBase: true}
	for i := 0; i < 3; i++ {
		var writeErr error
		if opt.EmptyComparison && i == 2 {
			writeErr = os.Remove(filepath.Join(repo, "note | μ.txt"))
		} else {
			writeErr = os.WriteFile(filepath.Join(repo, "note | μ.txt"), []byte(fmt.Sprintf("original %d | μ\r\n", i)), 0600)
		}
		if err := writeErr; err != nil {
			return nil, err
		}
		root := before.BaseCommit
		p, err := gitcommit.PersistCommitWithDiffs(ctx, gitcommit.PersistCommitActivity{RepoPath: repo, StorageLocation: filepath.Join(dir, "patches"), RootHash: root, ExpectedHeadHash: before.HeadCommit, EmptyBase: before.EmptyBase, CommitMessage: "saved original"}, root)
		if err != nil {
			return nil, err
		}
		read := func(path string) ([]byte, error) {
			if path == "" {
				return nil, nil
			}
			return os.ReadFile(path)
		}
		parent, err := read(p.DiffFromParentPath)
		if err != nil {
			return nil, err
		}
		base, err := read(p.DiffFromBasePath)
		if err != nil {
			return nil, err
		}
		bundle, err := read(p.ThinPackPath)
		if err != nil {
			return nil, err
		}
		after := before
		after.HeadCommit = p.CommitHash
		f.Steps = append(f.Steps, Step{Before: before, After: after, Parent: parent, Base: base, Bundle: bundle})
		before = after
	}
	// An unchanged point intentionally has no new patch or snapshot.
	f.Steps = append(f.Steps, Step{Before: before, After: before})
	if opt.Transform != nil {
		f.Steps = opt.Transform(f.Steps)
	}
	worker := &taskWorker{steps: f.Steps, unknownSizes: opt.UnknownSizes, retryTask: opt.RetryTask}
	jobWorker := wf.ExecutionWorker{JobType: "saved-diff-fixture", Execute: func(c wf.ExecutionContext, in wf.JobData) (wf.JobData, error) {
		var last jobdb.Artifact
		execute := func(c wf.ExecutionContext) (wf.JobData, error) {
			for i, step := range f.Steps {
				if opt.BeforeTask != nil {
					if err := opt.BeforeTask(i); err != nil {
						return nil, err
					}
				}
				command, _ := json.Marshal(map[string]any{"op": "saved_fixture", "step": worker.Name()})
				invoke := func(c wf.ExecutionContext) (wf.JobData, error) {
					call := wf.TaskCall{Site: "quoted/μ", TaskType: worker.Name(), ContractVersion: 1, Command: command}
					if opt.RetryTask != nil && *opt.RetryTask == i {
						call.Policy.Retry = jobdb.RetryPolicy{MaximumAttempts: 2, InitialInterval: jobdb.Duration(time.Millisecond)}
					}
					return c.DoTask(call, func(context.Context) (jobdb.TaskData, error) {
						g := gitstate.GlobalGitTaskContext{BaseRepo: step.Before.Repository, CellName: "saved", ResolvedBaseHash: step.Before.BaseCommit, ParentHash: step.Before.BaseCommit, PersistHash: step.Before.HeadCommit, Workspace: &contextual.WorkspaceContext{Cell: step.Before.Cell, ScopeID: step.Before.ScopeID, Initial: "empty"}}
						input := map[string]any{"context": g, "input": map[string]any{"index": i}}
						var arts []jobdb.Artifact
						if last == nil && opt.Incoming != nil {
							input["restore_artifact"] = opt.Incoming
						}
						if last != nil && !opt.OmitIncoming[i] {
							k, e := last.ArtifactKey()
							if e != nil {
								return nil, e
							}
							input["restore_artifact"] = &k
							if opt.ConflictingIncoming && i == len(f.Steps)-1 {
								conflict := k
								conflict.TaskOrdinal++
								input["restore_artifact"] = &conflict
							}
							arts = append(arts, last)
						}
						return jobdb.NewTaskData(input, arts...)
					})
				}
				var out wf.JobData
				var err error
				if opt.SplitScopes {
					out, err = c.DoScope("branch", wf.ScopeOptions{}, invoke)
				} else {
					out, err = invoke(c)
				}
				if err != nil {
					return nil, err
				}
				arts, err := out.GetArtifacts()
				if err != nil {
					return nil, err
				}
				for _, a := range arts {
					if a.Name() == gitstate.ThinPackArtifactName {
						last = a
					}
				}
			}
			return in, nil
		}
		if opt.Scope {
			return c.DoScope("saved/scope", wf.ScopeOptions{}, func(inner wf.ExecutionContext) (wf.JobData, error) { return execute(inner) })
		}
		return execute(c)
	}}
	root := contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: "example.test/saved"}, Workspace: &contextual.WorkspaceContext{Cell: "saved", ScopeID: "root", Initial: "empty"}}
	if opt.RootAmbiguous {
		root.Workspace.ScopeID = ""
	}
	h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "saved-tests", Cell: "saved", CellRepo: "example.test/saved", JobType: jobWorker.Name(), Data: jobdb.NewTaskDataOrPanic(map[string]any{"context": root})}})
	if err != nil {
		return nil, err
	}
	f.Job = h.JobKey
	run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: f.Job, JobWorker: jobWorker, TaskWorkers: []wf.TaskWorker{worker}, WorkerID: "saved-fixture", LeaseDuration: time.Minute})
	if err != nil {
		return nil, err
	}
	result, err := run.Run(nil)
	if err != nil {
		return nil, err
	}
	if result.Status != wf.JobRunCompleted {
		return nil, fmt.Errorf("fixture failed: %v", result.JobError)
	}
	if err = os.RemoveAll(repo); err != nil {
		return nil, err
	}
	if err = os.RemoveAll(filepath.Join(dir, "patches")); err != nil {
		return nil, err
	}
	return f, nil
}

type taskWorker struct {
	steps        []Step
	unknownSizes bool
	retryTask    *int
	retried      bool
}

func (*taskWorker) Name() string { return "saved_fixture:saved_fixture" }
func (w *taskWorker) Run(_ wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := in.GetData()
	if err != nil {
		return nil, err
	}
	var input struct {
		Input struct {
			Index int `json:"index"`
		} `json:"input"`
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	step := w.steps[input.Input.Index]
	payload := map[string]any{"workspace_before": step.Before, "workspace_after": step.After, "workspace_scope_id": step.After.ScopeID, "git": contextual.GitCommitContext{PersistHash: step.After.HeadCommit, ParentHash: step.After.BaseCommit}, "output": step.Output, "artifact_refs": step.Refs}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, payload)
	if err != nil {
		return nil, err
	}
	var arts []jobdb.Artifact
	patch := func(name string, data []byte) jobdb.Artifact {
		if w.unknownSizes {
			return jobdb.NewArtifactFromReader(name, bytes.NewReader(data), -1)
		}
		return jobdb.NewArtifactFromBytes(name, data)
	}
	if step.Bundle != nil {
		arts = append(arts, jobdb.NewArtifactFromBytes(gitstate.ThinPackArtifactName, step.Bundle))
	}
	if !step.Omit && step.Parent != nil {
		arts = append(arts, patch("diff_from_parent.diff", step.Parent))
	}
	if !step.Omit && !step.ParentOnly && step.Base != nil {
		arts = append(arts, patch("diff_from_base.diff", step.Base))
	}
	if step.Arbitrary {
		arts = append(arts, jobdb.NewArtifactFromBytes("arbitrary.diff", step.Parent))
	}
	out, err := jobdb.NewTaskData(env, arts...)
	if err == nil && w.retryTask != nil && *w.retryTask == input.Input.Index && !w.retried {
		w.retried = true
		return out, jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "native saved-diff task retry"}}
	}
	return out, err
}
