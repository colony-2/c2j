package contextual

import (
	"time"

	"github.com/colony-2/c2j/pkg/artifacts"
)

// WorktreePathSentinel is a placeholder value used during template resolution
// Templates that reference {{ environment.worktree_path }} will resolve to this sentinel
// The actual worktree path is determined at execution time on the target machine
const WorktreePathSentinel = "__C2_SENTINEL_WORKTREE__"
const WorkdirPathSentinel = "__C2_SENTINEL_WORKDIR__"
const ArtifactInboxSentinel = "__C2_SENTINEL_ARTIFACT_INBOX__"
const ArtifactOutboxSentinel = "__C2_SENTINEL_ARTIFACT_OUTBOX__"
const JobIdSentinel = "__C2_SENTINEL_JOB_ID__"
const OpWorkdirPathSentinel = "__C2_SENTINEL_OP_WORKDIR__"
const OpWorktreePathSentinel = "__C2_SENTINEL_OP_WORKTREE__"
const OpArtifactInboxSentinel = "__C2_SENTINEL_OP_ARTIFACT_INBOX__"
const OpArtifactOutboxSentinel = "__C2_SENTINEL_OP_ARTIFACT_OUTBOX__"

type EnvironmentPathContext struct {
	Workdir      string `json:"workdir,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"`
	Inbox        string `json:"inbox,omitempty"`
	Outbox       string `json:"outbox,omitempty"`
}

// EnvironmentContext captures filesystem and storage locations relevant to execution.
type EnvironmentContext struct {
	WorktreePath   string                 `json:"worktree_path,omitempty"`
	WorkdirPath    string                 `json:"workdir,omitempty"`
	ArtifactInbox  string                 `json:"inbox,omitempty"`
	ArtifactOutbox string                 `json:"outbox,omitempty"`
	Host           EnvironmentPathContext `json:"host,omitempty"`
	Op             EnvironmentPathContext `json:"op,omitempty"`
}

// GitBaseContext captures immutable git state information at a point in time.
type GitBaseContext struct {
	BaseRepo         string `json:"repo,omitempty"`
	BaseRef          string `json:"ref,omitempty"`
	ResolvedBaseHash string `json:"resolved_hash,omitempty"`
	GitAuthor        string `json:"author,omitempty"`
}

type RecipeSourceContext struct {
	Repo     string `json:"repo,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
	Selector string `json:"selector,omitempty"`
}

// WorkflowContext provides high-level workflow/session identifiers.
type WorkflowContext struct {
	CellID    string `json:"cell_id,omitempty"`
	CellName  string `json:"cell,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	ProjectId string `json:"project_id,omitempty"`
}

// ExecutionContext holds typed workflow context available to templates. It is created per task.
type JobContext struct {
	Environment  EnvironmentContext       `json:"environment,omitempty"`
	Artifacts    map[string]artifacts.Ref `json:"artifacts,omitempty"`
	Workflow     WorkflowContext          `json:"workflow,omitempty"`
	GitBase      GitBaseContext           `json:"git,omitempty"`
	RecipeSource RecipeSourceContext      `json:"recipe_source,omitempty"`
}

type TaskContext struct {
	// GitCommit is shared across entire ResolutionContext and often updated. Thus must be a pointer.
	GitCommit  *GitCommitContext
	Invocation Invocation
}

func NewTaskExecutionContext(ctx JobContext, ctx2 TaskContext) TaskExecutionContext {
	return TaskExecutionContext{
		Environment:  ctx.Environment,
		Artifacts:    cloneArtifactRefs(ctx.Artifacts),
		Workflow:     ctx.Workflow,
		RecipeSource: ctx.RecipeSource,
		GitTask: GitTask{
			BaseRepo:         ctx.GitBase.BaseRepo,
			BaseRef:          ctx.GitBase.BaseRef,
			ResolvedBaseHash: ctx.GitBase.ResolvedBaseHash,
			GitAuthor:        ctx.GitBase.GitAuthor,
			PersistHash:      ctx2.GitCommit.PersistHash,
			ParentHash:       ctx2.GitCommit.ParentHash,
			ParentRef:        ctx2.GitCommit.ParentRef,
		},
		Invocation: InvocationCtx{
			Hash:      GetInvocationHash(ctx2.Invocation),
			InvokeSeq: ctx2.Invocation.InvokeSeq,
			NodePath:  ctx2.Invocation.NodePath,
		},
	}
}

type TaskExecutionContext struct {
	// embed these directly from task and job contexts for easier resolution.
	Environment  EnvironmentContext       `json:"environment,omitempty"`
	Artifacts    map[string]artifacts.Ref `json:"artifacts,omitempty"`
	Workflow     WorkflowContext          `json:"workflow,omitempty"`
	RecipeSource RecipeSourceContext      `json:"recipe_source,omitempty"`
	GitTask      GitTask                  `json:"git,omitempty"`
	Invocation   InvocationCtx            `json:"invocation,omitempty"`
}

type GitTask struct {
	BaseRepo         string `json:"repo,omitempty"`
	BaseRef          string `json:"ref,omitempty"`
	ResolvedBaseHash string `json:"resolved_hash,omitempty"`
	GitAuthor        string `json:"author,omitempty"`
	ParentRef        string `json:"parent_ref,omitempty"`  // ref carrying workspace state until a hash exists
	PersistHash      string `json:"hash,omitempty"`        // materialized SHA after a commit is created
	ParentHash       string `json:"parent_hash,omitempty"` // parent SHA once materialized
}

type InvocationCtx struct {
	Hash      string `json:"hash"`
	NodePath  string `json:"path"`
	InvokeSeq int64  `json:"sequence"`
}

func (t TaskExecutionContext) JobContext() JobContext {
	return JobContext{
		Environment: t.Environment,
		Artifacts:   cloneArtifactRefs(t.Artifacts),
		Workflow:    t.Workflow,
		GitBase: GitBaseContext{
			BaseRepo:         t.GitTask.BaseRepo,
			BaseRef:          t.GitTask.BaseRef,
			ResolvedBaseHash: t.GitTask.ResolvedBaseHash,
			GitAuthor:        t.GitTask.GitAuthor,
		},
		RecipeSource: t.RecipeSource,
	}
}

func cloneArtifactRefs(in map[string]artifacts.Ref) map[string]artifacts.Ref {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]artifacts.Ref, len(in))
	for name, ref := range in {
		out[name] = ref
	}
	return out
}

// WorkspaceResult captures the output of inline/detached workspace executions.
type GitCommitContext struct {
	ParentRef   string `json:"parent_ref,omitempty"`  // ref carrying workspace state until a hash exists
	PersistHash string `json:"hash,omitempty"`        // materialized SHA after a commit is created
	ParentHash  string `json:"parent_hash,omitempty"` // parent SHA once materialized
}

type StepOutput struct {
	Outputs   map[string]interface{}   `json:"outputs"`
	Artifacts map[string]artifacts.Ref `json:"artifacts"`
	Runs      []RunOutput              `json:"runs"` // Previous runs (state loops)
}

// RunOutput represents a single execution run
type RunOutput struct {
	Outputs   map[string]interface{}   `json:"outputs"`
	Artifacts map[string]artifacts.Ref `json:"artifacts"`
	RunID     string                   `json:"run_id"`
	Timestamp time.Time                `json:"timestamp"`
}
