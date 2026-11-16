// Package changestest builds real writer-produced Git/JobDB fixtures for reader
// consumers. Build mutates only its supplied runtime and directory during setup.
// DeleteCheckout and independent Oracle calls make retained-object tests reusable.
package changestest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/gitcommit"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	wf "github.com/colony-2/jobdb/pkg/workflow"
)

type Options struct {
	Directory                       string
	Tenant, Cell, Repository, JobID string
	Format                          string
	Commits                         int
	Revert                          bool
	EmptyBase                       bool
	OmitBaseBundle                  bool
	MergeAt                         int // 1-based changed commit, or -1 for the excluded base
	Unchanged                       bool
	AdvanceBase                     bool
	Fallback                        bool
	Published                       bool
	Detached                        bool
	Rebase                          bool
	FastForward                     bool // build on a topic branch, then integrate with --ff-only
	WaitAt                          int  // final native external task; zero disables
	// BeforeTask runs after earlier tasks are recorded, for movement fixtures.
	BeforeTask func(int) error
	AfterTask  func(int) error
	// AfterStep can publish native external inputs between workspace records.
	// It is replayed with the containing job; use stable task sites and forms.
	AfterStep    func(wf.ExecutionContext, int) error
	RootContext  func(*contextual.JobContext)
	InputContext func(int, *gitstate.GlobalGitTaskContext)
	RestoreInput func(int) *jobdb.ArtifactKey
	// Transform supports malformed, missing, foreign-scope and failed fixtures
	// using original writer bytes and native workspace records.
	Transform func(int, *Step)
}

type Step struct {
	State  coretask.WorkspaceState
	Bundle []byte
	Fail   bool
	Git    *contextual.GitCommitContext
}

type Fixture struct {
	Job              jobdb.JobKey
	Base, Head       string
	Commits          []string
	Checkout, Origin string
	Steps            []Step
	Options          Options
	Worker           wf.ExecutionWorker
}

func Git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return GitInput(ctx, dir, nil, args...)
}

// GitInput runs fixture Git with explicit input and the same isolated identity
// and configuration as Git. It supports binary tree and pack fault fixtures.
func GitInput(ctx context.Context, dir string, input io.Reader, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	c.Stdin = input
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture | μ", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture | μ", "GIT_COMMITTER_EMAIL=fixture@example.test", "GIT_AUTHOR_DATE=2001-01-01T00:00:00+00:00", "GIT_COMMITTER_DATE=2001-01-01T00:00:00+00:00"}
	b, e := c.CombinedOutput()
	if e != nil {
		return nil, fmt.Errorf("fixture git %s: %w: %s", args[0], e, b)
	}
	return b, nil
}

func Build(ctx context.Context, rt jobdb.WorkflowRuntime, opt Options) (*Fixture, error) {
	if opt.Directory == "" {
		return nil, fmt.Errorf("fixture directory is required")
	}
	if opt.Tenant == "" {
		opt.Tenant = "tenant"
	}
	if opt.Cell == "" {
		opt.Cell = "changes"
	}
	if opt.Repository == "" {
		opt.Repository = "example.test/changes"
	}
	if opt.Format == "" {
		opt.Format = "sha1"
	}
	if opt.Commits == 0 {
		opt.Commits = 3
	}
	f := &Fixture{Checkout: filepath.Join(opt.Directory, "checkout"), Origin: filepath.Join(opt.Directory, "origin"), Options: opt}
	if err := os.MkdirAll(f.Checkout, 0700); err != nil {
		return nil, err
	}
	git := func(args ...string) (string, error) {
		b, e := Git(ctx, f.Checkout, args...)
		return strings.TrimSpace(string(b)), e
	}
	if _, err := git("init", "--quiet", "--object-format="+opt.Format); err != nil {
		return nil, err
	}
	for k, v := range map[string]string{"user.name": "Fixture | μ", "user.email": "fixture@example.test", "commit.gpgsign": "false"} {
		if _, err := git("config", k, v); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(f.Checkout, "note.txt"), []byte("base\n"), 0600); err != nil {
		return nil, err
	}
	persist := func(base, expected string, empty bool, message string) (*gitcommit.PersistCommitOutput, error) {
		return gitcommit.PersistCommit(ctx, gitcommit.PersistCommitActivity{RepoPath: f.Checkout, StorageLocation: filepath.Join(opt.Directory, "packs"), RootHash: base, ExpectedHeadHash: expected, EmptyBase: empty, CommitMessage: message, Timeout: 10 * time.Second})
	}
	base, err := persist("", "", true, "base")
	if err != nil {
		return nil, err
	}
	f.Base = base.CommitHash
	merge := func(label string) error {
		if _, e := git("checkout", "-q", "-b", "side-"+label); e != nil {
			return e
		}
		if e := os.WriteFile(filepath.Join(f.Checkout, "side-"+label), []byte("side\n"), 0600); e != nil {
			return e
		}
		if _, e := git("add", "."); e != nil {
			return e
		}
		if _, e := git("commit", "-q", "-m", "side"); e != nil {
			return e
		}
		if _, e := git("checkout", "-q", "-"); e != nil {
			return e
		}
		_, e := git("merge", "--no-ff", "-m", "merge", "side-"+label)
		return e
	}
	if opt.MergeAt == -1 {
		if err = merge("base"); err != nil {
			return nil, err
		}
		base, err = persist("", f.Base, true, "merged base")
		if err != nil {
			return nil, err
		}
		f.Base = base.CommitHash
	}
	baseBytes, err := os.ReadFile(base.ThinPackPath)
	if err != nil {
		return nil, err
	}
	f.Steps = append(f.Steps, Step{State: coretask.WorkspaceState{Repository: opt.Repository, Cell: opt.Cell, ScopeID: "main", EmptyBase: true, HeadCommit: f.Base}, Bundle: baseBytes})
	if opt.OmitBaseBundle {
		f.Steps[0].Bundle = nil
	}
	if _, err = Git(ctx, opt.Directory, "clone", "--quiet", "--bare", f.Checkout, f.Origin); err != nil {
		return nil, err
	}
	previous := f.Base
	integrationBranch := ""
	if opt.FastForward {
		integrationBranch, err = git("symbolic-ref", "--short", "HEAD")
		if err != nil {
			return nil, err
		}
		if _, err = git("checkout", "-q", "-b", "candidate"); err != nil {
			return nil, err
		}
	}
	if opt.Detached {
		if _, err = git("checkout", "--detach", "--quiet", f.Base); err != nil {
			return nil, err
		}
	}
	for i := 1; i <= opt.Commits; i++ {
		if opt.Rebase && i == opt.Commits {
			if _, err = git("commit", "--amend", "--no-edit", "--reset-author"); err != nil {
				return nil, err
			}
		}
		value := fmt.Sprintf("change %d | μ\n", i)
		if opt.Revert && i == opt.Commits {
			value = "base\n"
		}
		if err = os.WriteFile(filepath.Join(f.Checkout, "note.txt"), []byte(value), 0600); err != nil {
			return nil, err
		}
		if !opt.Revert {
			if err = os.WriteFile(filepath.Join(f.Checkout, "binary.dat"), []byte{0, 255, byte(i)}, 0600); err != nil {
				return nil, err
			}
		}
		if opt.MergeAt == i {
			if _, err = git("add", "."); err != nil {
				return nil, err
			}
			if _, err = git("commit", "-q", "-m", "linear before merge"); err != nil {
				return nil, err
			}
			if err = merge(fmt.Sprint(i)); err != nil {
				return nil, err
			}
		}
		if opt.MergeAt != i {
			if _, err = git("add", "-A"); err != nil {
				return nil, err
			}
			if _, err = git("commit", "--quiet", "--date="+fmt.Sprintf("2001-01-%02dT00:00:00+00:00", 28-i%27), "-m", fmt.Sprintf("change %d | μ", i)); err != nil {
				return nil, err
			}
		}
		root, empty := f.Base, false
		if opt.AdvanceBase {
			root = previous
		}
		if opt.Fallback {
			root, err = git("rev-parse", "HEAD")
			if err != nil {
				return nil, err
			}
		}
		if opt.EmptyBase {
			root = ""
			empty = true
		}
		out, e := persist(root, previous, empty, fmt.Sprintf("change %d | μ", i))
		if e != nil {
			return nil, e
		}
		data, e := os.ReadFile(out.ThinPackPath)
		if e != nil {
			return nil, e
		}
		f.Commits = append(f.Commits, out.CommitHash)
		f.Head = out.CommitHash
		previous = f.Head
		f.Steps = append(f.Steps, Step{State: coretask.WorkspaceState{Repository: opt.Repository, Cell: opt.Cell, ScopeID: "main", BaseCommit: root, EmptyBase: empty, HeadCommit: f.Head}, Bundle: data})
	}
	if opt.FastForward {
		if _, err = git("checkout", "-q", integrationBranch); err != nil {
			return nil, err
		}
		if _, err = git("merge", "--ff-only", "candidate"); err != nil {
			return nil, err
		}
	}
	if opt.Unchanged {
		last := f.Steps[len(f.Steps)-1]
		last.Bundle = nil
		f.Steps = append(f.Steps, last)
	}
	if opt.Published {
		last := f.Steps[len(f.Steps)-1]
		last.Bundle = nil
		last.State.BaseCommit = last.State.HeadCommit
		last.State.EmptyBase = false
		f.Steps = append(f.Steps, last)
	}
	for i := range f.Steps {
		if opt.Transform != nil {
			opt.Transform(i, &f.Steps[i])
		}
	}
	worker := &fixtureWorker{steps: f.Steps}
	jobWorker := wf.ExecutionWorker{JobType: "changes-fixture", Execute: func(c wf.ExecutionContext, in wf.JobData) (wf.JobData, error) {
		for i, step := range f.Steps {
			if opt.BeforeTask != nil {
				if err := opt.BeforeTask(i); err != nil {
					return nil, err
				}
			}
			g := gitstate.GlobalGitTaskContext{NodePath: fmt.Sprintf("step/%d", i), BaseRepo: step.State.Repository, BaseRef: "main", ResolvedBaseHash: step.State.BaseCommit, ParentHash: step.State.BaseCommit, PersistHash: step.State.HeadCommit, Workspace: &contextual.WorkspaceContext{Cell: opt.Cell, ScopeID: step.State.ScopeID}}
			if step.State.EmptyBase {
				g.Workspace.Initial = "empty"
			}
			if opt.InputContext != nil {
				opt.InputContext(i, &g)
			}
			taskType := worker.Name()
			if opt.WaitAt > 0 && opt.WaitAt == i {
				taskType = "changes:external"
			}
			_, err := c.DoTask(wf.TaskCall{Site: "step", TaskType: taskType, ContractVersion: 1, Command: json.RawMessage(`{}`), Policy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}, func(context.Context) (jobdb.TaskData, error) {
				input := map[string]any{"context": g}
				if opt.RestoreInput != nil {
					input["restore_artifact"] = opt.RestoreInput(i)
				}
				return jobdb.NewTaskData(input)
			})
			if err != nil {
				return nil, err
			}
			if opt.AfterTask != nil {
				if err := opt.AfterTask(i); err != nil {
					return nil, err
				}
			}
			if opt.AfterStep != nil {
				if err := opt.AfterStep(c, i); err != nil {
					return nil, err
				}
			}
		}
		return in, nil
	}}
	f.Worker = jobWorker
	rootContext := contextual.JobContext{GitBase: contextual.GitBaseContext{BaseRepo: opt.Repository}, Workspace: &contextual.WorkspaceContext{Cell: opt.Cell, ScopeID: "main"}}
	if opt.RootContext != nil {
		opt.RootContext(&rootContext)
	}
	input := map[string]any{"context": rootContext}
	handle, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: opt.Tenant, JobID: opt.JobID, JobType: jobWorker.Name(), Cell: opt.Cell, CellRepo: opt.Repository, Data: jobdb.NewTaskDataOrPanic(input), RunPolicy: jobdb.RunPolicy{Retry: jobdb.RetryPolicy{MaximumAttempts: 1}}}})
	if err != nil {
		return nil, err
	}
	f.Job = handle.JobKey
	run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: f.Job, JobWorker: jobWorker, TaskWorkers: []wf.TaskWorker{worker}, WorkerID: "changes-fixture", LeaseDuration: time.Minute})
	if err != nil {
		return nil, err
	}
	_, err = run.Run(nil)
	if err != nil {
		return nil, err
	}
	return f, nil
}

type fixtureWorker struct {
	steps []Step
	next  int
}

func (*fixtureWorker) Name() string { return "changes:fixture" }
func (w *fixtureWorker) Run(tc wf.TaskContext, in jobdb.TaskData) (jobdb.TaskData, error) {
	raw, err := in.GetData()
	if err != nil {
		return nil, err
	}
	var input struct {
		Context gitstate.GlobalGitTaskContext `json:"context"`
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	var index int
	if _, err = fmt.Sscanf(input.Context.NodePath, "step/%d", &index); err != nil || index < 0 || index >= len(w.steps) {
		return nil, fmt.Errorf("invalid fixture step")
	}
	step := w.steps[index]
	w.next = index + 1
	git := contextual.GitCommitContext{ParentHash: step.State.BaseCommit, PersistHash: step.State.HeadCommit}
	if step.Git != nil {
		git = *step.Git
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, map[string]any{"workspace_after": step.State, "workspace_scope_id": step.State.ScopeID, "git": git, "output": map[string]any{"step": w.next}})
	if err != nil {
		return nil, err
	}
	var arts []jobdb.Artifact
	if step.Bundle != nil {
		arts = append(arts, jobdb.NewArtifactFromBytes(gitstate.ThinPackArtifactName, step.Bundle))
	}
	data, err := jobdb.NewTaskData(env, arts...)
	if err != nil {
		return nil, err
	}
	if step.Fail {
		return data, fmt.Errorf("fixture failed after recording workspace")
	}
	return data, nil
}

// Oracle computes the documented ordinary two-tree binary patch independently.
// Call it before DeleteCheckout; expected bytes remain owned by the consumer.
func (f *Fixture) Oracle(ctx context.Context, from, through string) ([]byte, error) {
	return Git(ctx, f.Checkout, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", "--src-prefix=a/", "--dst-prefix=b/", "--no-color", from, through, "--")
}
func (f *Fixture) DeleteCheckout() error { return os.RemoveAll(f.Checkout) }

// RunRecorded reuses the native worker against a replay/restart job without
// rebuilding Git data. Completed records keep their original artifact keys.
func (f *Fixture) RunRecorded(ctx context.Context, rt jobdb.WorkflowRuntime, key jobdb.JobKey) error {
	run, err := wf.GetJobForRun(ctx, rt, wf.GetJobForRunRequest{JobKey: key, JobWorker: f.Worker, TaskWorkers: []wf.TaskWorker{&fixtureWorker{steps: f.Steps}}, WorkerID: "changes-replay", LeaseDuration: time.Minute})
	if err != nil {
		return err
	}
	_, err = run.Run(nil)
	return err
}
