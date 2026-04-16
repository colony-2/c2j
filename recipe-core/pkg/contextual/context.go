package contextual

import (
	"time"

	"github.com/colony-2/c2j/recipe-core/pkg/artifacts"
)

// WorktreePathSentinel is a placeholder value used during template resolution
// Templates that reference {{ environment.worktree_path }} will resolve to this sentinel
// The actual worktree path is determined at execution time on the target machine
const WorktreePathSentinel = "__C2_SENTINEL_WORKTREE__"
const WorkdirPathSentinel = "__C2_SENTINEL_WORKDIR__"
const ArtifactInboxSentinel = "__C2_SENTINEL_ARTIFACT_INBOX__"
const ArtifactOutboxSentinel = "__C2_SENTINEL_ARTIFACT_OUTBOX__"
const JobIdSentinel = "__C2_SENTINEL_JOB_ID__"

// ActorContext represents the user/cell identity associated with an invocation.
type ActorContext struct {
	TicketID   string `json:"ticket_id,omitempty"`
	ActorName  string `json:"actor_name,omitempty"`
	ActorEmail string `json:"actor_email,omitempty"`
}

// EnvironmentContext captures filesystem and storage locations relevant to execution.
type EnvironmentContext struct {
	WorktreePath   string `json:"worktree_path,omitempty"`
	WorkdirPath    string `json:"workdir,omitempty"`
	ArtifactInbox  string `json:"inbox,omitempty"`
	ArtifactOutbox string `json:"outbox,omitempty"`
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
	CellPath  string `json:"cell_path,omitempty"` // Cell relative path from repo root
	JobID     string `json:"job_id,omitempty"`
	ProjectId string `json:"project_id,omitempty"`
}

type TicketCreatorUserContext struct {
	Email string `json:"email,omitempty"`
}

type TicketCreatorAgentContext struct {
	CellName       string `json:"cell,omitempty"`
	WorkflowName   string `json:"workflow_name,omitempty"`
	ExecutionID    string `json:"execution_id,omitempty"`
	InvocationHash string `json:"invocation_hash,omitempty"`
}

type TicketCreatorContext struct {
	Type  string                     `json:"type,omitempty"`
	User  *TicketCreatorUserContext  `json:"user,omitempty"`
	Agent *TicketCreatorAgentContext `json:"agent,omitempty"`
}

// TicketContext represents ticket metadata available to recipes.
type TicketContext struct {
	ID          string               `json:"id,omitempty"`
	Title       string               `json:"title,omitempty"`
	Description string               `json:"description,omitempty"`
	Creator     TicketCreatorContext `json:"creator,omitempty"`
	CreatedAt   time.Time            `json:"created_at,omitempty"`
	UpdatedAt   time.Time            `json:"updated_at,omitempty"`
}

// ExecutionContext holds typed workflow context available to templates. It is created per task.
type JobContext struct {
	Actor        ActorContext        `json:"actor,omitempty"`
	Ticket       TicketContext       `json:"ticket,omitempty"`
	Environment  EnvironmentContext  `json:"environment,omitempty"`
	Workflow     WorkflowContext     `json:"workflow,omitempty"`
	GitBase      GitBaseContext      `json:"git,omitempty"`
	RecipeSource RecipeSourceContext `json:"recipe_source,omitempty"`
}

type TaskContext struct {
	// GitCommit is shared across entire ResolutionContext and often updated. Thus must be a pointer.
	GitCommit  *GitCommitContext
	Invocation Invocation
}

func NewTaskExecutionContext(ctx JobContext, ctx2 TaskContext) TaskExecutionContext {
	return TaskExecutionContext{
		Actor:        ctx.Actor,
		Ticket:       ctx.Ticket,
		Environment:  ctx.Environment,
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
	Actor        ActorContext        `json:"actor,omitempty"`
	Ticket       TicketContext       `json:"ticket,omitempty"`
	Environment  EnvironmentContext  `json:"environment,omitempty"`
	Workflow     WorkflowContext     `json:"workflow,omitempty"`
	RecipeSource RecipeSourceContext `json:"recipe_source,omitempty"`
	GitTask      GitTask             `json:"git,omitempty"`
	Invocation   InvocationCtx       `json:"invocation,omitempty"`
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
		Actor:       t.Actor,
		Ticket:      t.Ticket,
		Environment: t.Environment,
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
