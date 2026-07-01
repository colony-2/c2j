package ops

import (
	"context"
	"errors"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"gorm.io/gorm"
)

type OpDependencies interface {
	Database() *gorm.DB
	WorkflowControl() workflowctl.WorkflowControl
	GetInputArtifacts() []jobdb.Artifact
	AddOutputArtifact(jobdb.Artifact) error
	AddExternalArtifact(name string, url string, expand bool) error
	GetOutputArtifacts() []jobdb.Artifact
	GetExternalArtifacts() map[string]recipeartifacts.Ref
	WorktreePath() string
	GitContext() GitExecutionContext
	CurrentJobContext() jobcontext.Current
	ProtectedEnv() map[string]string
	JobTool() JobTool
	FindArtifact(key jobdb.ArtifactKey) (jobdb.Artifact, error)
	SetNextTaskType(taskType string)
}

// JobTool provides a way to influence the current jobs operation. It is separate from workflowcontrol, which is about running jobs independent of this jobs context.
type JobTool interface {
	GetJobKey() jobdb.JobKey
	AwaitJobs(jobIds ...string) error
}

type TaskBasedJobTool struct {
	TaskContext jobworkflow.TaskContext
}

func (j *TaskBasedJobTool) GetJobKey() jobdb.JobKey {
	return j.TaskContext.JobKey
}

func (j *TaskBasedJobTool) AwaitJobs(jobIds ...string) error {
	return j.TaskContext.AwaitJobs(jobIds...)
}

func (j *TaskBasedJobTool) SubmitJob(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	return j.TaskContext.SubmitJob(ctx, job)
}

// opDepImpl holds the actual dependencies.
type opDepImpl struct {
	db                *gorm.DB
	inputArtifacts    []jobdb.Artifact
	outputArtifacts   []jobdb.Artifact
	externalArtifacts map[string]recipeartifacts.Ref
	workflowControl   workflowctl.WorkflowControl
	worktreePath      string
	operationPaths    OperationPaths
	pathRuntime       OperationPathRuntime
	gitContext        GitExecutionContext
	currentJobContext jobcontext.Current
	protectedEnv      map[string]string
	jobTool           JobTool
	nextTaskType      string
	nextTaskTypeSet   bool
}

func (c *opDepImpl) FindArtifact(key jobdb.ArtifactKey) (jobdb.Artifact, error) {
	var found jobdb.Artifact
	for _, artifact := range c.GetInputArtifacts() {
		if artifact.Name() == key.Name {
			if found != nil {
				return nil, errors.New("duplicate artifact found")
			}
			found = artifact
		}
	}

	if found == nil {
		return nil, errors.New("artifact not found")
	}
	return found, nil
}

func (c *opDepImpl) GetOutputArtifacts() []jobdb.Artifact {
	out := make([]jobdb.Artifact, len(c.outputArtifacts))
	copy(out, c.outputArtifacts)
	return out
}

func (c *opDepImpl) AddExternalArtifact(name string, url string, expand bool) error {
	artifactRef := recipeartifacts.NewExternalRef(name, url, expand)
	if err := artifactRef.Validate(); err != nil {
		return err
	}
	if c.externalArtifacts == nil {
		c.externalArtifacts = make(map[string]recipeartifacts.Ref)
	}
	c.externalArtifacts[name] = artifactRef
	return nil
}

func (c *opDepImpl) GetExternalArtifacts() map[string]recipeartifacts.Ref {
	if len(c.externalArtifacts) == 0 {
		return nil
	}
	out := make(map[string]recipeartifacts.Ref, len(c.externalArtifacts))
	for name, artifactRef := range c.externalArtifacts {
		out[name] = artifactRef
	}
	return out
}

func (c *opDepImpl) JobTool() JobTool {
	return c.jobTool
}

// Database implements the OpDependencies interface.
func (c *opDepImpl) Database() *gorm.DB {
	return c.db
}

// AddArtifact implements the OpDependencies interface.
func (c *opDepImpl) AddOutputArtifact(a jobdb.Artifact) error {
	if a == nil {
		return errors.New("cannot add nil artifact")
	}
	c.outputArtifacts = append(c.outputArtifacts, a)
	// Log/side effect removed for simplicity
	return nil
}

// GetInputArtifacts implements the OpDependencies interface.
func (c *opDepImpl) GetInputArtifacts() []jobdb.Artifact {
	return c.inputArtifacts
}

// WorkflowControl implements the OpDependencies interface.
func (c *opDepImpl) WorkflowControl() workflowctl.WorkflowControl {
	return c.workflowControl
}

// WorktreePath implements the OpDependencies interface.
func (c *opDepImpl) WorktreePath() string {
	return c.worktreePath
}

func (c *opDepImpl) OperationPaths() OperationPaths {
	return c.operationPaths
}

func (c *opDepImpl) OperationPathRuntime() OperationPathRuntime {
	return c.pathRuntime
}

func (c *opDepImpl) GitContext() GitExecutionContext {
	return c.gitContext
}

func (c *opDepImpl) CurrentJobContext() jobcontext.Current {
	return c.currentJobContext
}

func (c *opDepImpl) ProtectedEnv() map[string]string {
	if len(c.protectedEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.protectedEnv))
	for key, value := range c.protectedEnv {
		out[key] = value
	}
	return out
}

func (c *opDepImpl) SetNextTaskType(taskType string) {
	c.nextTaskType = taskType
	c.nextTaskTypeSet = true
}

func (c *opDepImpl) NextTaskType() (string, bool) {
	return c.nextTaskType, c.nextTaskTypeSet
}

type OpDependenciesBuilder struct {
	db                *gorm.DB
	artifacts         []jobdb.Artifact
	workflowControl   workflowctl.WorkflowControl
	worktreePath      string
	operationPaths    OperationPaths
	pathRuntime       OperationPathRuntime
	gitContext        GitExecutionContext
	currentJobContext jobcontext.Current
	protectedEnv      map[string]string
	jobTool           JobTool
}

// NewOpDependenciesBuilder creates a new, empty builder instance.
func NewOpDependenciesBuilder() *OpDependenciesBuilder {
	return &OpDependenciesBuilder{
		artifacts: make([]jobdb.Artifact, 0), // Initialize inputArtifacts slice
	}
}

func (b *OpDependenciesBuilder) WithDatabase(db *gorm.DB) *OpDependenciesBuilder {
	b.db = db
	return b
}

func (b *OpDependenciesBuilder) WithTaskContext(tc jobworkflow.TaskContext) *OpDependenciesBuilder {
	b.jobTool = &TaskBasedJobTool{tc}
	return b
}

func (b *OpDependenciesBuilder) WithJobTool(jt JobTool) *OpDependenciesBuilder {
	b.jobTool = jt
	return b
}

func (b *OpDependenciesBuilder) WithArtifacts(initialArtifacts []jobdb.Artifact) *OpDependenciesBuilder {
	if initialArtifacts == nil {
		b.artifacts = make([]jobdb.Artifact, 0)
		return b
	}
	out := make([]jobdb.Artifact, len(initialArtifacts))
	copy(out, initialArtifacts)
	b.artifacts = out
	return b
}

func (b *OpDependenciesBuilder) WithWorkflowControl(wc workflowctl.WorkflowControl) *OpDependenciesBuilder {
	b.workflowControl = wc
	return b
}

func (b *OpDependenciesBuilder) WithWorktreePath(path string) *OpDependenciesBuilder {
	b.worktreePath = path
	return b
}

func (b *OpDependenciesBuilder) WithOperationPaths(paths OperationPaths) *OpDependenciesBuilder {
	b.operationPaths = paths
	if b.worktreePath == "" {
		b.worktreePath = paths.WorktreePath
	}
	return b
}

func (b *OpDependenciesBuilder) WithOperationPathRuntime(runtime OperationPathRuntime) *OpDependenciesBuilder {
	b.pathRuntime = runtime
	return b
}

func (b *OpDependenciesBuilder) WithGitContext(ctx GitExecutionContext) *OpDependenciesBuilder {
	b.gitContext = ctx
	if b.worktreePath == "" {
		b.worktreePath = ctx.WorktreePath
	}
	return b
}

func (b *OpDependenciesBuilder) WithCurrentJobContext(ctx jobcontext.Current) *OpDependenciesBuilder {
	b.currentJobContext = ctx
	return b
}

func (b *OpDependenciesBuilder) WithProtectedEnv(env map[string]string) *OpDependenciesBuilder {
	if len(env) == 0 {
		b.protectedEnv = nil
		return b
	}
	b.protectedEnv = make(map[string]string, len(env))
	for key, value := range env {
		b.protectedEnv[key] = value
	}
	return b
}

func (b *OpDependenciesBuilder) Build() OpDependencies {
	protectedEnv := b.protectedEnv
	if protectedEnv == nil {
		protectedEnv = jobcontext.EnvForCurrent(b.currentJobContext)
	}
	protectedEnvCopy := make(map[string]string, len(protectedEnv))
	for key, value := range protectedEnv {
		protectedEnvCopy[key] = value
	}
	deps := &opDepImpl{
		db:                b.db,
		inputArtifacts:    b.artifacts,
		workflowControl:   b.workflowControl,
		worktreePath:      b.worktreePath,
		operationPaths:    b.operationPaths,
		pathRuntime:       b.pathRuntime,
		gitContext:        b.gitContext,
		currentJobContext: b.currentJobContext,
		protectedEnv:      protectedEnvCopy,
		outputArtifacts:   make([]jobdb.Artifact, 0),
		externalArtifacts: make(map[string]recipeartifacts.Ref),
		jobTool:           b.jobTool,
	}

	return deps
}
