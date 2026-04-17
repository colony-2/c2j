package ops

import (
	"errors"

	recipeartifacts "github.com/colony-2/c2j/pkg/core/artifacts"
	"github.com/colony-2/c2j/pkg/core/workflowctl"
	"github.com/colony-2/swf-go/pkg/swf"
	"gorm.io/gorm"
)

type OpDependencies interface {
	Database() *gorm.DB
	WorkflowControl() workflowctl.WorkflowControl
	GetInputArtifacts() []swf.Artifact
	AddOutputArtifact(swf.Artifact) error
	AddExternalArtifact(name string, url string, expand bool) error
	GetOutputArtifacts() []swf.Artifact
	GetExternalArtifacts() map[string]recipeartifacts.Ref
	WorktreePath() string
	GitContext() GitExecutionContext
	JobTool() JobTool
	FindArtifact(key swf.ArtifactKey) (swf.Artifact, error)
	SetNextTaskType(taskType string)
}

// JobTool provides a way to influence the current jobs operation. It is separate from workflowcontrol, which is about running jobs independent of this jobs context.
type JobTool interface {
	GetJobKey() swf.JobKey
	AwaitJobs(jobIds ...string) error
}

type TaskBasedJobTool struct {
	TaskContext swf.TaskContext
}

func (j *TaskBasedJobTool) GetJobKey() swf.JobKey {
	return j.TaskContext.JobKey
}

func (j *TaskBasedJobTool) AwaitJobs(jobIds ...string) error {
	return j.TaskContext.AwaitJobs(jobIds...)
}

// opDepImpl holds the actual dependencies.
type opDepImpl struct {
	db                *gorm.DB
	inputArtifacts    []swf.Artifact
	outputArtifacts   []swf.Artifact
	externalArtifacts map[string]recipeartifacts.Ref
	workflowControl   workflowctl.WorkflowControl
	worktreePath      string
	gitContext        GitExecutionContext
	jobTool           JobTool
	nextTaskType      string
	nextTaskTypeSet   bool
}

func (c *opDepImpl) FindArtifact(key swf.ArtifactKey) (swf.Artifact, error) {
	var found swf.Artifact
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

func (c *opDepImpl) GetOutputArtifacts() []swf.Artifact {
	out := make([]swf.Artifact, len(c.outputArtifacts))
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
func (c *opDepImpl) AddOutputArtifact(a swf.Artifact) error {
	if a == nil {
		return errors.New("cannot add nil artifact")
	}
	c.outputArtifacts = append(c.outputArtifacts, a)
	// Log/side effect removed for simplicity
	return nil
}

// GetInputArtifacts implements the OpDependencies interface.
func (c *opDepImpl) GetInputArtifacts() []swf.Artifact {
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

func (c *opDepImpl) GitContext() GitExecutionContext {
	return c.gitContext
}

func (c *opDepImpl) SetNextTaskType(taskType string) {
	c.nextTaskType = taskType
	c.nextTaskTypeSet = true
}

func (c *opDepImpl) NextTaskType() (string, bool) {
	return c.nextTaskType, c.nextTaskTypeSet
}

type OpDependenciesBuilder struct {
	db              *gorm.DB
	artifacts       []swf.Artifact
	workflowControl workflowctl.WorkflowControl
	worktreePath    string
	gitContext      GitExecutionContext
	jobTool         JobTool
}

// NewOpDependenciesBuilder creates a new, empty builder instance.
func NewOpDependenciesBuilder() *OpDependenciesBuilder {
	return &OpDependenciesBuilder{
		artifacts: make([]swf.Artifact, 0), // Initialize inputArtifacts slice
	}
}

func (b *OpDependenciesBuilder) WithDatabase(db *gorm.DB) *OpDependenciesBuilder {
	b.db = db
	return b
}

func (b *OpDependenciesBuilder) WithTaskContext(tc swf.TaskContext) *OpDependenciesBuilder {
	b.jobTool = &TaskBasedJobTool{tc}
	return b
}

func (b *OpDependenciesBuilder) WithJobTool(jt JobTool) *OpDependenciesBuilder {
	b.jobTool = jt
	return b
}

func (b *OpDependenciesBuilder) WithArtifacts(initialArtifacts []swf.Artifact) *OpDependenciesBuilder {
	if initialArtifacts == nil {
		b.artifacts = make([]swf.Artifact, 0)
		return b
	}
	out := make([]swf.Artifact, len(initialArtifacts))
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

func (b *OpDependenciesBuilder) WithGitContext(ctx GitExecutionContext) *OpDependenciesBuilder {
	b.gitContext = ctx
	if b.worktreePath == "" {
		b.worktreePath = ctx.WorktreePath
	}
	return b
}

func (b *OpDependenciesBuilder) Build() OpDependencies {
	deps := &opDepImpl{
		db:                b.db,
		inputArtifacts:    b.artifacts,
		workflowControl:   b.workflowControl,
		worktreePath:      b.worktreePath,
		gitContext:        b.gitContext,
		outputArtifacts:   make([]swf.Artifact, 0),
		externalArtifacts: make(map[string]recipeartifacts.Ref),
		jobTool:           b.jobTool,
	}

	return deps
}
