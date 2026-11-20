package workspacedifftest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

type OrdinaryOptions struct {
	OmitBase         bool
	CommandOperation string
	Contract         uint32
	WrongRoot        bool
}

// BuildOrdinary invokes the built-in persistence implementation in a native
// task, followed by the real automatic diff writer. It retains the built-in's
// actual-parent output, unlike ordinary opaque operation output. The second
// task has a parent distinct from the cumulative base.
func BuildOrdinary(ctx context.Context, rt jobdb.ConsumerRuntime, dir string, opt OrdinaryOptions) (*Fixture, error) {
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Saved fixture"}, {"config", "user.email", "fixture@example.test"}} {
		if _, err := changestest.Git(ctx, repo, args...); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "original"), []byte("base\n"), 0600); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "base"}} {
		if _, err := changestest.Git(ctx, repo, args...); err != nil {
			return nil, err
		}
	}
	raw, err := changestest.Git(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	base := strings.TrimSpace(string(raw))
	state := coretask.WorkspaceState{Repository: "example.test/saved", Cell: "saved", ScopeID: "root", BaseCommit: base, HeadCommit: base}
	f := &Fixture{Directory: dir}
	worker := &ordinaryWorker{repo: repo, dir: dir, state: state, fixture: f, omitBase: opt.OmitBase}
	job := wf.ExecutionWorker{JobType: "saved-ordinary", Execute: func(c wf.ExecutionContext, in wf.JobData) (wf.JobData, error) {
		var incoming jobdb.Artifact
		for range 2 {
			commandOp := opt.CommandOperation
			if commandOp == "" {
				commandOp = "git_persist_commit"
			}
			command, _ := json.Marshal(map[string]any{"op": commandOp, "step": worker.Name(), "recipe": strings.Repeat("a", 64), "node": "ordinary/recorded"})
			contract := opt.Contract
			if contract == 0 {
				contract = 1
			}
			out, err := c.DoTask(wf.TaskCall{Site: "persist", TaskType: worker.Name(), ContractVersion: contract, Command: command}, func(context.Context) (jobdb.TaskData, error) {
				g := gitstate.GlobalGitTaskContext{BaseRepo: state.Repository, CellName: state.Cell, NodePath: "ordinary/recorded", ResolvedBaseHash: base, ParentHash: base, PersistHash: worker.state.HeadCommit, Workspace: &contextual.WorkspaceContext{Cell: state.Cell, ScopeID: state.ScopeID}}
				root := base
				if opt.WrongRoot {
					root = strings.Repeat("0", 40)
				}
				input := map[string]any{"context": g, "workspace_managed": true, "input": map[string]any{"repo_path": contextual.WorktreePathSentinel, "root_hash": root}}
				var arts []jobdb.Artifact
				if incoming != nil {
					k, err := incoming.ArtifactKey()
					if err != nil {
						return nil, err
					}
					input["restore_artifact"] = &k
					arts = append(arts, incoming)
				}
				return jobdb.NewTaskData(input, arts...)
			})
			if err != nil {
				return nil, err
			}
			arts, err := out.GetArtifacts()
			if err != nil {
				return nil, err
			}
			for _, a := range arts {
				if a.Name() == gitstate.ThinPackArtifactName {
					incoming = a
				}
			}
		}
		return in, nil
	}}
	root := contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: state.Repository}, Workspace: &contextual.WorkspaceContext{Cell: state.Cell, ScopeID: state.ScopeID}}
	h, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "saved-tests", Cell: "saved", CellRepo: state.Repository, JobType: job.Name(), Data: jobdb.NewTaskDataOrPanic(map[string]any{"context": root})}})
	if err != nil {
		return nil, err
	}
	f.Job = h.JobKey
	run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: f.Job, JobWorker: job, TaskWorkers: []wf.TaskWorker{worker}, WorkerID: "ordinary", LeaseDuration: time.Minute})
	if err != nil {
		return nil, err
	}
	outcome, err := run.Run(nil)
	if err != nil {
		return nil, err
	}
	if outcome.Status != wf.JobRunCompleted {
		return nil, fmt.Errorf("ordinary fixture failed: %v", outcome.JobError)
	}
	if err := os.RemoveAll(repo); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(filepath.Join(dir, "patches")); err != nil {
		return nil, err
	}
	return f, nil
}

type ordinaryWorker struct {
	repo, dir string
	state     coretask.WorkspaceState
	fixture   *Fixture
	omitBase  bool
}

func (*ordinaryWorker) Name() string { return "git_persist_commit:git_persist_commit" }
func (w *ordinaryWorker) Run(c wf.TaskContext, _ jobdb.TaskData) (jobdb.TaskData, error) {
	before := w.state
	if err := os.WriteFile(filepath.Join(w.repo, "original"), []byte(fmt.Sprintf("ordinary %d\n", len(w.fixture.Steps))), 0600); err != nil {
		return nil, err
	}
	original, err := (&gitcommit.PersistCommitActivityWrapper{}).Execute(nil, c.Context(), gitcommit.PersistCommitInput{RepoPath: w.repo, StorageLocation: filepath.Join(w.dir, "patches"), RootHash: before.BaseCommit})
	if err != nil {
		return nil, err
	}
	diff, err := gitcommit.PersistCommitWithDiffs(c.Context(), gitcommit.PersistCommitActivity{RepoPath: w.repo, StorageLocation: filepath.Join(w.dir, "patches"), RootHash: before.BaseCommit, ExpectedHeadHash: before.HeadCommit}, before.BaseCommit)
	if err != nil {
		return nil, err
	}
	read := func(path string) ([]byte, error) {
		if path == "" {
			return nil, nil
		}
		return os.ReadFile(path)
	}
	parent, err := read(diff.DiffFromParentPath)
	if err != nil {
		return nil, err
	}
	base, err := read(diff.DiffFromBasePath)
	if err != nil {
		return nil, err
	}
	bundle, err := read(diff.ThinPackPath)
	if err != nil {
		return nil, err
	}
	w.state.HeadCommit = original.CommitHash
	w.fixture.Steps = append(w.fixture.Steps, Step{Before: before, After: w.state, Parent: parent, Base: base, Bundle: bundle})
	envelope, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"workspace_before": before, "workspace_after": w.state, "workspace_scope_id": w.state.ScopeID, "git": contextual.GitCommitContext{PersistHash: w.state.HeadCommit, ParentHash: w.state.BaseCommit}, "output": original})
	if err != nil {
		return nil, err
	}
	var arts []jobdb.Artifact
	for name, data := range map[string][]byte{gitstate.ThinPackArtifactName: bundle, "diff_from_parent.diff": parent, "diff_from_base.diff": base} {
		if data != nil && !(w.omitBase && name == "diff_from_base.diff") {
			arts = append(arts, jobdb.NewArtifactFromBytes(name, data))
		}
	}
	return jobdb.NewTaskData(envelope, arts...)
}
