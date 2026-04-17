package ops

import (
	"context"
	"fmt"
	"os"
	"reflect"

	recipeartifacts "github.com/colony-2/c2j/pkg/core/artifacts"
	"github.com/colony-2/c2j/pkg/core/contextual"
	"github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/git/gitstate"
	"github.com/colony-2/c2j/pkg/worker/activity"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/invopop/jsonschema"
)

const thinPackSentinel = gitstate.ThinPackArtifactName

// ActivityInvocationRequest wraps the invocation metadata and original input payload.
type ActivityInvocationRequest struct {
	Input          map[string]interface{}         `json:"input"`
	Const          bool                           `json:"const,omitempty"`
	GitTaskContext gitstate.GlobalGitTaskContext  `json:"context"`
	ArtifactKeys   []swf.ArtifactKey              `json:"artifact_keys,omitempty"`
	Artifacts      map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
	Deps           ops.OpDependencies             `json:"-"`
}

// ActivityInvocationOutput wraps the raw op output alongside workspace results.
type ActivityInvocationOutput struct {
	GitResult    contextual.GitCommitContext    `json:"git,omitempty"`
	NextTask     string                         `json:"nextTaskType,omitempty"`
	OpOutput     map[string]interface{}         `json:"output"`
	ArtifactRefs map[string]recipeartifacts.Ref `json:"artifact_refs,omitempty"`
}

// variation of ActivityInvocationOutput that allows arbitrary output types to avoid double serialization
type ActivityInvocationOutputRaw struct {
	GitResult    contextual.GitCommitContext    `json:"git,omitempty"`
	NextTask     string                         `json:"nextTaskType,omitempty"`
	Output       any                            `json:"output"`
	ArtifactRefs map[string]recipeartifacts.Ref `json:"artifact_refs,omitempty"`
}

// ActivityRegistration holds the activity step and its generated schemas.
type ActivityRegistration struct {
	Activity     ops.RegisterableOp // parent op
	Step         ops.TaskStep
	StepIndex    int
	TaskType     string // opType:stepName
	NextTaskType string
	InputSchema  *jsonschema.Schema
	OutputSchema *jsonschema.Schema
	Metadata     ops.OpMetadata
}

// ActivityRegistry manages all registered activities
type ActivityRegistry struct {
	activities    map[string]ActivityRegistration
	generator     SchemaGenerator
	gitController *gitstate.Controller
	deps          ops.ServiceDependencies2
}

// SchemaGenerator validates struct tags and generates JSON schemas
// This is internal to recipe-worker
type SchemaGenerator interface {
	GenerateSchema(typ reflect.Type) (*jsonschema.Schema, error)
	ValidateStructTags(typ reflect.Type) error
}

// NewActivityRegistry creates a new activity registry
func NewActivityRegistry() (*ActivityRegistry, error) {
	a := &ActivityRegistry{
		activities:    make(map[string]ActivityRegistration),
		generator:     NewDefaultSchemaGenerator(),
		gitController: gitstate.NewController(nil),
		deps:          ops.NewServiceDepsBuilder().Build(),
	}
	opsList := ops.List()
	for _, op := range opsList {
		if err := a.register(op); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// SetDependencies makes a dependency container available for invocations produced by this registry.
func (r *ActivityRegistry) SetDependencies(deps ops.ServiceDependencies2) {
	r.deps = deps
}

// Dependencies exposes the current dependency container (may be nil).
func (r *ActivityRegistry) Dependencies() ops.ServiceDependencies2 {
	return r.deps
}

func (r *ActivityRegistry) GetTaskWorkers(deps ops.ServiceDependencies2) []swf.TaskWorker {
	workers := make([]swf.TaskWorker, 0, len(r.activities))
	for name, registration := range r.activities {
		if registration.Step.DisallowAsTask {
			continue
		}
		controller := r.gitController
		if controller == nil {
			gitstate.NewController(nil)
		}
		doer := &opExecutor{deps: deps, reg: registration, controller: controller}
		workers = append(workers, &taskWorker{name: name, reg: registration, doer: doer})
	}
	return workers
}

// RegisterGeneric registers any activity without knowing its specific generic types
// This allows dynamic registration of activities from external packages
func (r *ActivityRegistry) register(activity ops.RegisterableOp) error {
	return Register(r, activity)
}

func (r *ActivityRegistry) Register(activity ops.RegisterableOp) error {
	return Register(r, activity)
}

type GenericActivityFunc func(context.Context, any) (any, error)

func (r *ActivityRegistry) RegisterFunc(name string, fn func(context.Context, map[string]interface{}) (map[string]interface{}, error)) error {
	fnB := func(_ ops.OpDependencies, ctx context.Context, input map[string]interface{}) (map[string]interface{}, error) {
		return fn(ctx, input)
	}
	op := ops.NewActivityMappedOpV2[map[string]interface{}, map[string]interface{}](ops.OpMetadata{Type: name}, fnB)
	return Register(r, op)
}

type ActivityRegisterable interface {
	RegisterActivityWithOptions(a interface{}, options activity.RegisterOptions)
}

func (r *ActivityRegistry) EnableActivitiesInWorker(deps ops.ServiceDependencies2, worker ActivityRegisterable) {
	for name, registration := range r.activities {
		doer := opExecutor{deps: deps, reg: registration, controller: r.gitController}
		worker.RegisterActivityWithOptions(doer.do, activity.RegisterOptions{Name: name})
	}
}

// Register accepts any generic RegisterableOp from the activity module
func Register(r *ActivityRegistry, activity ops.RegisterableOp) error {
	metadata := activity.GetMetadata()

	chain := activity.TaskChain()
	for i, step := range chain {
		if step.Name == "" {
			return fmt.Errorf("step %d for op %s must have a name", i, metadata.Type)
		}
		if step.InputType == nil || step.OutputType == nil {
			return fmt.Errorf("step %s for op %s has nil input/output type", step.Name, metadata.Type)
		}
		taskType := fmt.Sprintf("%s:%s", metadata.Type, step.Name)
		if _, exists := r.activities[taskType]; exists {
			return fmt.Errorf("activity type %s already registered", taskType)
		}

		if err := r.generator.ValidateStructTags(step.InputType); err != nil {
			return fmt.Errorf("input type validation failed: %w", err)
		}
		if err := r.generator.ValidateStructTags(step.OutputType); err != nil {
			return fmt.Errorf("output type validation failed: %w", err)
		}

		if step.NextStepTask == "" && i < len(chain)-1 {
			step.NextStepTask = fmt.Sprintf("%s:%s", metadata.Type, chain[i+1].Name)
		}

		inputSchema, err := r.generator.GenerateSchema(step.InputType)
		if err != nil {
			return fmt.Errorf("input schema generation failed: %w", err)
		}

		outputSchema, err := r.generator.GenerateSchema(step.OutputType)
		if err != nil {
			return fmt.Errorf("output schema generation failed: %w", err)
		}

		r.activities[taskType] = ActivityRegistration{
			Activity:     activity,
			Step:         step,
			StepIndex:    i,
			TaskType:     taskType,
			NextTaskType: step.NextStepTask,
			InputSchema:  inputSchema,
			OutputSchema: outputSchema,
			Metadata:     metadata,
		}
	}

	return nil
}

// Get retrieves an activity registration by task type
func (r *ActivityRegistry) Get(taskType string) (ActivityRegistration, bool) {
	registration, exists := r.activities[taskType]
	return registration, exists
}

// List returns all registered activity types
func (r *ActivityRegistry) List() []string {
	types := make([]string, 0, len(r.activities))
	for activityType := range r.activities {
		types = append(types, activityType)
	}
	return types
}

// GetAll returns all activity registrations
func (r *ActivityRegistry) GetAll() map[string]ActivityRegistration {
	// Return a copy to prevent external modification
	result := make(map[string]ActivityRegistration)
	for k, v := range r.activities {
		result[k] = v
	}
	return result
}

// UpdateRegistration updates an existing activity registration
// This is used by the schema manager to update schemas after generation
func (r *ActivityRegistry) UpdateRegistration(activityType string, registration ActivityRegistration) {
	r.activities[activityType] = registration
}

// createWorkDir creates a temporary directory for git worktree
func createWorkDir() (string, error) {
	return os.MkdirTemp("", "recipe-worktree-*")
}

// removeWorkDir removes the temporary worktree directory
func removeWorkDir(path string) {
	_ = os.RemoveAll(path)
}
