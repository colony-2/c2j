package template

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
	exprpb "google.golang.org/genproto/googleapis/api/expr/v1alpha1"
	"google.golang.org/protobuf/types/known/structpb"
)

// ScopeType defines resolver scope kinds.
type ScopeType string

const (
	ScopeRecipe       ScopeType = "recipe"
	ScopeSequence     ScopeType = "sequence"
	ScopeStateMachine ScopeType = "state_machine"
	ScopeState        ScopeType = "state"
	ScopeOp           ScopeType = "op"
)

// templateData is the root context for both Go templates and CEL
type templateData struct {
	ContainerInputs map[string]interface{}          `json:"container_inputs"` // ContainerInputs map if this is a sequence or state machine.
	Sequence        map[string]StepOutput           `json:"sequence"`         // Sibling nodes in sequence
	States          map[string]StepOutput           `json:"states"`           // Completed states in state machine
	Vars            map[string]interface{}          `json:"vars"`             // Scoped computed values
	Outputs         map[string]interface{}          `json:"outputs"`          // Current source outputs for transition evaluation/payloads.
	Transition      TransitionData                  `json:"transition"`       // Transition metadata visible to a target state invocation.
	Scope           ScopeMetadata                   `json:"scope"`            // Execution metadata
	Context         contextual.TaskExecutionContext `json:"context"`          // Execution context (typed)
}

type StepOutput = contextual.StepOutput

// RunOutput represents a single execution run
type RunOutput = contextual.RunOutput

// ScopeMetadata contains execution context metadata
type ScopeMetadata struct {
	ExecutionID string    `json:"execution_id"`
	Timestamp   time.Time `json:"timestamp"`
	// Note: No attempts/runs counter - ambiguous which level
}

// ResolutionContext represents template resolution context
type ResolutionContext struct {
	commitContext *contextual.GitCommitContext

	// Scope type: "root", "sequence", "state_machine", "state"
	ScopeType ScopeType
	Options   ResolutionOptions
	// EffectiveConst is inherited down the node tree and prevents git state advancement.
	EffectiveConst bool

	scopeId string

	// Parent context (nil for root)
	Parent *ResolutionContext

	// Template data for current scope
	TemplateData templateData

	// CEL environment for when expressions
	CELEnv *cel.Env

	CurrentRunID string // Track current run for this scope

	tracker *invocationTracker

	lastExecution    map[string]interface{}
	lastArtifacts    []swf.Artifact
	lastArtifactRefs []recipeartifacts.Ref
	artifactCache    map[string]swf.Artifact
}

func (rc *ResolutionContext) UpdateGitState(commit contextual.GitCommitContext) {
	rc.commitContext.ParentRef = commit.ParentRef
	rc.commitContext.ParentHash = commit.ParentHash
	rc.commitContext.PersistHash = commit.PersistHash
}

func (rc *ResolutionContext) GetGitCommitContext() contextual.GitCommitContext {
	return *rc.commitContext
}

// NewRecipeResolutionContext creates a new resolution context for a recipe
func NewRecipeResolutionContext(commitContext *contextual.GitCommitContext, recipeInputs map[string]interface{}, execCtx contextual.JobContext, opts ...ResolutionOptions) (*ResolutionContext, error) {
	tracker := newInvocationTracker()

	options := DefaultResolutionOptions()
	if len(opts) > 0 {
		options = opts[0]
	}
	// Default builtin set if caller did not supply CEL options.
	if options.CELOptionsProvider == nil {
		builder := funcregistry.NewBuilder().WithDefaults()
		options.CELOptionsProvider = builder
	}

	return newResolutionContext(commitContext, tracker, ScopeRecipe, "", recipeInputs, execCtx, options)
}

func newResolutionContext(commitContext *contextual.GitCommitContext, tracker *invocationTracker, scopeType ScopeType, scopeId string, containerInputs map[string]interface{}, execCtx contextual.JobContext, options ResolutionOptions) (*ResolutionContext, error) {
	rc := &ResolutionContext{
		commitContext: commitContext,
		ScopeType:     scopeType,
		Options:       options,
		tracker:       tracker,
		scopeId:       scopeId,
		TemplateData: templateData{

			ContainerInputs: containerInputs,
			Sequence:        make(map[string]StepOutput),
			States:          make(map[string]StepOutput),
			Vars:            make(map[string]interface{}),
			Outputs:         make(map[string]interface{}),
			Transition:      EmptyTransitionData(),
			Scope: ScopeMetadata{
				ExecutionID: generateExecutionID(),
				Timestamp:   time.Now(),
			},
			Context: contextual.NewTaskExecutionContext(execCtx, contextual.TaskContext{
				Invocation: tracker.nextInvocation(),
				GitCommit:  commitContext,
			}),
		},
		artifactCache: make(map[string]swf.Artifact),
	}

	// Initialize CEL environment
	// Note: We use a custom type adapter to properly handle Go struct embedding
	// CEL doesn't natively understand Go's anonymous struct fields, so we need to
	// provide a flattened view that matches JSON serialization behavior
	baseOpts := []cel.EnvOption{
		cel.OptionalTypes(),
		cel.Variable("inputs", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("sequence", cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.DynType))),
		cel.Variable("states", cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.DynType))),
		cel.Variable("vars", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("outputs", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("transition", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("scope", cel.MapType(cel.StringType, cel.DynType)),
		cel.Variable("context", cel.ObjectType("contextual.TaskExecutionContext")),
		ext.NativeTypes(
			reflect.TypeOf(StepOutput{}),
			reflect.TypeOf(RunOutput{}),
			reflect.TypeOf(contextual.TaskExecutionContext{}),
			reflect.TypeOf(contextual.JobContext{}),
			reflect.TypeOf(contextual.TaskContext{}),
			reflect.TypeOf(contextual.EnvironmentContext{}),
			reflect.TypeOf(contextual.WorkflowContext{}),
			reflect.TypeOf(contextual.GitBaseContext{}),
			reflect.TypeOf(contextual.GitCommitContext{}),
			reflect.TypeOf(contextual.Invocation{}),
			reflect.TypeOf(recipeartifacts.Ref{}),
			reflect.TypeOf(recipeartifacts.StoredRef{}),
			reflect.TypeOf(recipeartifacts.ExternalRef{}),
			reflect.TypeOf(swf.ArtifactKey{}),
			ext.ParseStructTag("json"),
		),
	}

	// Allow provider to contribute type options before env creation.
	if options.CELOptionsProvider != nil {
		if typeOpts := options.CELOptionsProvider.TypeOptions(); len(typeOpts) > 0 {
			baseOpts = append(baseOpts, typeOpts...)
		}
	}

	env, err := cel.NewEnv(baseOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}
	adapter := newResolutionTypeAdapter(env.CELTypeAdapter(), options)
	env, err = cel.CustomTypeAdapter(adapter)(env)
	if err != nil {
		return nil, fmt.Errorf("failed to configure CEL adapter: %w", err)
	}

	// Inject registry-provided options (default or caller supplied)
	var extraOpts []cel.EnvOption
	if options.CELOptionsProvider != nil {
		type contextualProvider interface {
			FunctionOptionsWithContext(types.Adapter, funcregistry.ContextProvider) ([]cel.EnvOption, error)
		}
		ctxProvider := func() contextual.TaskExecutionContext {
			return rc.TemplateData.Context
		}
		if cp, ok := options.CELOptionsProvider.(contextualProvider); ok {
			extraOpts, err = cp.FunctionOptionsWithContext(adapter, ctxProvider)
		} else {
			extraOpts, err = options.CELOptionsProvider.FunctionOptions(adapter)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get CEL options: %w", err)
		}
	}
	extraOpts = append(extraOpts, rc.stateLookupEnvOptions(adapter)...)
	if len(extraOpts) > 0 {
		env, err = env.Extend(extraOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to extend CEL env: %w", err)
		}
	}
	rc.CELEnv = env
	rc.ensureContextBackfill()

	return rc, nil
}

func (rc *ResolutionContext) TaskExecutionContext() contextual.TaskExecutionContext {
	return rc.TemplateData.Context
}

func jsonParseEnvOption(adapter types.Adapter) cel.EnvOption {
	return cel.Function(
		"json_parse",
		cel.Overload(
			"json_parse_dyn",
			[]*cel.Type{cel.DynType},
			cel.DynType,
			cel.UnaryBinding(jsonParseBinding(adapter)),
		),
	)
}

func jsonParseBinding(adapter types.Adapter) func(ref.Val) ref.Val {
	return func(value ref.Val) ref.Val {
		if value == nil {
			return types.NewErr("json_parse: expected string")
		}

		raw, ok := value.(types.String)
		if !ok {
			strValue, ok := value.Value().(string)
			if !ok {
				return types.NewErr("json_parse: expected string")
			}
			raw = types.String(strValue)
		}

		input := string(raw)
		if strings.TrimSpace(input) == "" {
			return types.NewErr("json_parse: expected string")
		}

		var decoded interface{}
		if err := json.Unmarshal([]byte(input), &decoded); err != nil {
			return types.NewErr("json_parse: invalid JSON: %v", err)
		}

		return adapter.NativeToValue(decoded)
	}
}

func scopeId(meta recipe.NodeMetadata, fallback string, scopeType ScopeType) string {
	if meta.ID != "" {
		return meta.ID
	} else if fallback != "" {
		return fallback
	} else {
		switch scopeType {
		case ScopeOp:
			return "op"
		case ScopeState:
			return "state"
		case ScopeRecipe:
			return "recipe"
		case ScopeStateMachine:
			return "state_machine"
		case ScopeSequence:
			return "sequence"
		default:
			panic(fmt.Sprintf("invalid scope type: %s", scopeType))
		}
	}
}

// ScopeID exposes the internal scope id logic for validation pre-seeding.
func ScopeID(meta recipe.NodeMetadata, fallback string, scopeType ScopeType) string {
	return scopeId(meta, fallback, scopeType)
}

// NewChildContext creates a child resolution context
func (rc *ResolutionContext) NewChildContext(scopeType ScopeType, metadata recipe.NodeMetadata, fallback string, inputs map[string]interface{}) (*ResolutionContext, error) {
	switch scopeType {
	case ScopeRecipe, ScopeSequence, ScopeStateMachine:
		if inputs == nil {
			return nil, fmt.Errorf("inputs cannot be nil for root or sequence scope")
		}
	default:
		if inputs != nil {
			return nil, fmt.Errorf("inputs cannot be set for state or op scope")
		}
	}

	scopeId := scopeId(metadata, fallback, scopeType)
	child, err := newResolutionContext(rc.commitContext, rc.tracker.child(scopeId), scopeType, scopeId, inputs, rc.TaskExecutionContext().JobContext(), rc.Options)
	if err != nil {
		return nil, err
	}
	child.EffectiveConst = rc.EffectiveConst || metadata.Const
	child.TemplateData.Vars = cloneTemplateVars(rc.TemplateData.Vars)
	child.TemplateData.Transition = rc.TemplateData.Transition.Clone()

	// copy items from parents based on scope.
	switch scopeType {
	case ScopeRecipe, ScopeStateMachine:
		// new scope, copy nothing down.

	case ScopeSequence:
		// new scope, copy nothing down.

	case ScopeState:
		// child state can see the whole state machine's state and the state machine inputs.
		// containers are not cloned because we want to be able to add to them and have parent context see where appropriate.
		child.TemplateData.ContainerInputs = rc.TemplateData.ContainerInputs
		child.TemplateData.States = rc.TemplateData.States
	case ScopeOp:
		// op can see their container's inputs as well as states and sequences of their container.
		// containers are not cloned because we want to be able to add to them and have parent context see where appropriate.
		child.TemplateData.ContainerInputs = rc.TemplateData.ContainerInputs
		child.TemplateData.States = rc.TemplateData.States
		child.TemplateData.Sequence = rc.TemplateData.Sequence
	}

	child.Parent = rc
	rc.ensureContextBackfill()
	return child, nil
}

// ensureContextBackfill keeps the template data context initialized even when callers omit it.
func (rc *ResolutionContext) ensureContextBackfill() {
	if rc.TemplateData.Sequence == nil {
		rc.TemplateData.Sequence = make(map[string]StepOutput)
	}
	if rc.TemplateData.States == nil {
		rc.TemplateData.States = make(map[string]StepOutput)
	}
	if rc.TemplateData.ContainerInputs == nil {
		rc.TemplateData.ContainerInputs = make(map[string]interface{})
	}
	if rc.TemplateData.Vars == nil {
		rc.TemplateData.Vars = make(map[string]interface{})
	}
	if rc.TemplateData.Outputs == nil {
		rc.TemplateData.Outputs = make(map[string]interface{})
	}
	if rc.TemplateData.Transition.Payload == nil {
		rc.TemplateData.Transition.Payload = make(map[string]interface{})
	}
}

func (rc *ResolutionContext) ResolveMap(input map[string]interface{}) (map[string]interface{}, error) {
	resolved := make(map[string]interface{})
	for key, value := range input {
		resolvedValue, err := rc.resolveValue(value)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve input '%s': %w", key, err)
		}
		resolved[key] = resolvedValue
	}
	return resolved, nil
}

// resolveTemplate handles expression evaluation using CEL with interpolation support
func (rc *ResolutionContext) resolveTemplate(expr string) (interface{}, error) {
	// Use the new interpolation mode by default for backward compatibility
	return rc.interpolateString(expr, ModeInterpolation)
}

// EvaluateCEL handles pure CEL evaluation for when conditions
func (rc *ResolutionContext) EvaluateCEL(expr string) (bool, error) {
	if expr == "" || strings.ToLower(expr) == "true" {
		return true, nil
	}

	out, err := rc.evaluateCELExpression(expr)
	if err != nil {
		return false, err
	}
	boolOut, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression did not return bool")
	}
	return boolOut, nil
}

// evaluateCELExpression evaluates a CEL expression and returns any type
func (rc *ResolutionContext) evaluateCELExpression(expr string) (interface{}, error) {
	// Handle empty expressions
	if expr == "" {
		return "", nil
	}
	if rc.Options.ClampSliceIndex {
		expr = clampNumericIndexes(expr)
	}

	ast, issues := rc.CELEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("failed to compile CEL expression: %w", issues.Err())
	}

	// In validate mode avoid executing user functions; if any are present, return a placeholder.
	if rc.Options.Mode == ModeValidate && hasForbiddenCalls(ast.Expr()) {
		return placeholderFromCELType(ast.OutputType()), nil
	}

	program, err := rc.CELEnv.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL program: %w", err)
	}

	// Pass templateData fields as CEL variables with native types
	result, _, err := program.Eval(map[string]interface{}{
		"inputs":     rc.TemplateData.ContainerInputs,
		"sequence":   rc.celSequenceValue(),
		"states":     rc.celStatesValue(),
		"vars":       rc.TemplateData.Vars,
		"outputs":    rc.TemplateData.Outputs,
		"transition": rc.TemplateData.Transition.AsMap(),
		"scope":      rc.TemplateData.Scope,
		"context":    rc.TemplateData.Context,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate CEL expression: %w", err)
	}

	value := result.Value()
	if _, ok := value.(structpb.NullValue); ok {
		return nil, nil
	}
	return value, nil
}

// resolveValue recursively resolves templates in a value (uses interpolation mode by default)
func (rc *ResolutionContext) resolveValue(value interface{}) (interface{}, error) {
	return rc.ResolveValueWithMode(value, ModeInterpolation)
}

func (rc *ResolutionContext) AddExecution(output map[string]interface{}) {
	rc.AddExecutionWithArtifacts(output, nil)
}

func (rc *ResolutionContext) AddExecutionWithArtifactData(output map[string]interface{}, artifactRefs map[string]recipeartifacts.Ref, artifacts []swf.Artifact) {
	rc.lastExecution = output
	if artifactRefs == nil {
		artifactRefs = map[string]recipeartifacts.Ref{}
	}
	if len(artifacts) > 0 {
		rc.RememberArtifacts(artifacts)
	}

	refList := make([]recipeartifacts.Ref, 0, len(artifactRefs))
	capHint := len(artifactRefs)
	if len(artifacts) > capHint {
		capHint = len(artifacts)
	}
	artList := make([]swf.Artifact, 0, capHint)
	for _, artifactRef := range artifactRefs {
		refList = append(refList, artifactRef)
	}
	if len(artifacts) > 0 {
		artList = append(artList, artifacts...)
	} else {
		for _, artifactRef := range artifactRefs {
			if key, ok := artifactRef.StoredKey(); ok {
				if artifact, found := rc.artifactCache[recipeartifacts.NewStoredRef(key).Identity()]; found {
					artList = append(artList, artifact)
				}
			}
		}
	}
	rc.lastArtifactRefs = refList
	rc.lastArtifacts = artList

	var container map[string]StepOutput
	key := rc.scopeId

	switch rc.ScopeType {
	case ScopeSequence:
		container = rc.TemplateData.Sequence
	case ScopeStateMachine, ScopeState:
		container = rc.TemplateData.States
	case ScopeRecipe:
		return
	case ScopeOp:
		switch rc.Parent.ScopeType {
		case ScopeSequence:
			container = rc.TemplateData.Sequence
			rc.Parent.lastExecution = output
			rc.Parent.lastArtifactRefs = append([]recipeartifacts.Ref(nil), refList...)
			rc.Parent.lastArtifacts = append([]swf.Artifact(nil), artList...)
		case ScopeStateMachine, ScopeState:
			container = rc.TemplateData.States
			rc.Parent.lastExecution = output
			rc.Parent.lastArtifactRefs = append([]recipeartifacts.Ref(nil), refList...)
			rc.Parent.lastArtifacts = append([]swf.Artifact(nil), artList...)
			if rc.Parent.ScopeType == ScopeState {
				key = rc.Parent.scopeId
			}
		case ScopeRecipe:
			rc.Parent.lastExecution = output
			rc.Parent.lastArtifactRefs = append([]recipeartifacts.Ref(nil), refList...)
			rc.Parent.lastArtifacts = append([]swf.Artifact(nil), artList...)
			return
		default:
			panic(fmt.Sprintf("invalid parent scope type: %s", rc.Parent.ScopeType))
		}
	default:
		panic(fmt.Sprintf("invalid parent scope type: %s", rc.Parent.ScopeType))
	}

	if existing, ok := container[key]; ok {
		existing.Runs = append(existing.Runs, RunOutput{
			Outputs:   existing.Outputs,
			Artifacts: existing.Artifacts,
			RunID:     generateRunID(),
			Timestamp: time.Now(),
		})
		existing.Outputs = output
		existing.Artifacts = artifactRefs
		container[key] = existing
	} else {
		container[key] = StepOutput{
			Outputs:   output,
			Artifacts: artifactRefs,
			Runs:      []RunOutput{},
		}
	}
}

func (rc *ResolutionContext) RememberArtifacts(artifacts []swf.Artifact) {
	for _, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		key, err := artifact.ArtifactKey()
		if err != nil {
			continue
		}
		rc.artifactCache[recipeartifacts.NewStoredRef(key).Identity()] = artifact
	}
}

func (rc *ResolutionContext) AddExecutionWithArtifacts(output map[string]interface{}, artifactRefs map[string]recipeartifacts.Ref) {
	rc.AddExecutionWithArtifactData(output, artifactRefs, nil)
}

func (rc *ResolutionContext) GetLastExecution() map[string]interface{} {
	return rc.lastExecution
}

func (rc *ResolutionContext) GetLastArtifacts() []swf.Artifact {
	return rc.lastArtifacts
}

func (rc *ResolutionContext) GetLastArtifactRefs() []recipeartifacts.Ref {
	return rc.lastArtifactRefs
}

// validateTemplateReferences validates all template references before execution
func (rc *ResolutionContext) validateTemplateReferences(expr string) error {
	// Check if it's a template expression
	trimmed := strings.TrimSpace(expr)
	if !strings.HasPrefix(trimmed, "${{") || !strings.HasSuffix(trimmed, "}}") {
		return nil // Not a template
	}

	// Extract and validate the CEL expression
	innerExpr := strings.TrimSpace(trimmed[3 : len(trimmed)-2])
	return rc.validateCELExpression(innerExpr)
}

// validateCELExpression validates a CEL expression
func (rc *ResolutionContext) validateCELExpression(expr string) error {
	if expr == "" || strings.ToLower(expr) == "true" {
		return nil
	}

	_, issues := rc.CELEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("invalid CEL expression: %w", issues.Err())
	}

	return nil
}

// embeddedStructAdapter is a custom CEL type adapter that handles Go struct embedding
// by flattening anonymous embedded fields to match JSON serialization behavior
type embeddedStructAdapter struct {
	types.Adapter
}

func newEmbeddedStructAdapter() *embeddedStructAdapter {
	return &embeddedStructAdapter{
		Adapter: types.DefaultTypeAdapter,
	}
}

func (a *embeddedStructAdapter) NativeToValue(value interface{}) ref.Val {
	// Check if this is a TaskExecutionContext that needs flattening
	if ctx, ok := value.(contextual.TaskExecutionContext); ok {
		// Convert to JSON and back to get the flattened structure
		data, err := json.Marshal(ctx)
		if err != nil {
			return types.NewErr("failed to marshal context: %v", err)
		}
		var flattened map[string]interface{}
		if err := json.Unmarshal(data, &flattened); err != nil {
			return types.NewErr("failed to unmarshal context: %v", err)
		}
		return a.Adapter.NativeToValue(flattened)
	}

	// For all other types, use the default adapter
	return a.Adapter.NativeToValue(value)
}

// Helper functions for ID generation
func generateExecutionID() string {
	return fmt.Sprintf("exec-%d", time.Now().UnixNano())
}

func generateRunID() string {
	return fmt.Sprintf("run-%d", time.Now().UnixNano())
}

// placeholderFromCELType returns a benign placeholder value for the given CEL type.
// It is used during validation to avoid executing CEL functions on synthetic data.
func placeholderFromCELType(t *types.Type) interface{} {
	if t == nil {
		return nil
	}

	switch t.Kind() {
	case types.NullTypeKind, types.DynKind:
		return nil
	case types.BoolKind:
		return false
	case types.IntKind, types.UintKind, types.DoubleKind:
		return 0
	case types.StringKind, types.BytesKind:
		return ""
	case types.ListKind:
		return []interface{}{}
	case types.MapKind:
		return map[string]interface{}{}
	case types.TypeKind, types.StructKind, types.AnyKind:
		return map[string]interface{}{}
	default:
		return nil
	}
}

// hasForbiddenCalls detects whether the expression contains function calls that should not
// be executed during validation (non-operator function calls).
func hasForbiddenCalls(expr *exprpb.Expr) bool {
	if expr == nil {
		return false
	}

	switch e := expr.ExprKind.(type) {
	case *exprpb.Expr_CallExpr:
		fn := e.CallExpr.Function
		// Operators are represented with names like _&&_, _?_:_, etc. Allow those.
		if !strings.HasPrefix(fn, "_") {
			return true
		}
		if hasForbiddenCalls(e.CallExpr.Target) {
			return true
		}
		for _, arg := range e.CallExpr.Args {
			if hasForbiddenCalls(arg) {
				return true
			}
		}
	case *exprpb.Expr_SelectExpr:
		return hasForbiddenCalls(e.SelectExpr.Operand)
	case *exprpb.Expr_ListExpr:
		for _, el := range e.ListExpr.Elements {
			if hasForbiddenCalls(el) {
				return true
			}
		}
	case *exprpb.Expr_StructExpr:
		for _, entry := range e.StructExpr.Entries {
			if hasForbiddenCalls(entry.GetValue()) {
				return true
			}
			switch k := entry.KeyKind.(type) {
			case *exprpb.Expr_CreateStruct_Entry_FieldKey:
				// field keys are plain strings; nothing to check
				_ = k
			case *exprpb.Expr_CreateStruct_Entry_MapKey:
				if hasForbiddenCalls(k.MapKey) {
					return true
				}
			}
		}
	case *exprpb.Expr_ComprehensionExpr:
		// Comprehensions may execute user functions; conservatively forbid.
		return true
	}
	return false
}
