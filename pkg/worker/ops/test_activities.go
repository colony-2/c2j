package ops

import (
	"context"

	recipeops "github.com/colony-2/c2j/pkg/core/ops"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/colony-2/c2j/pkg/worker/sleepop"
	"github.com/colony-2/swf-go/pkg/swf"
)

// Test activity input/output types
type GenericInput struct {
	Message  string                 `json:"message,omitempty"`
	Command  string                 `json:"command,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Run      string                 `json:"run,omitempty"`
	Duration string                 `json:"duration,omitempty"`
	Data     interface{}            `json:"data,omitempty"`
	Items    []interface{}          `json:"items,omitempty"`
	Error    bool                   `json:"error,omitempty"`
	Extra    map[string]interface{} `json:"-" mapstructure:",remain"`
}

type GenericOutput struct {
	// String outputs
	Result    interface{} `json:"result,omitempty"`
	Output    string      `json:"output,omitempty"`
	Status    string      `json:"status,omitempty"`
	Stdout    string      `json:"stdout,omitempty"`
	Report    string      `json:"report,omitempty"`
	Response  string      `json:"response,omitempty"`
	Body      string      `json:"body,omitempty"`
	Slept     string      `json:"slept,omitempty"`
	Action    string      `json:"action,omitempty"`
	Handler   string      `json:"handler,omitempty"`
	Processor string      `json:"processor,omitempty"`
	Recipe    string      `json:"recipe,omitempty"`

	// Boolean outputs
	Logged bool `json:"logged,omitempty"`
	Valid  bool `json:"valid,omitempty"`

	// Numeric outputs
	Count      int     `json:"count,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`

	// Complex outputs
	Model interface{}   `json:"model,omitempty"`
	Data  interface{}   `json:"data,omitempty"`
	Items []interface{} `json:"items,omitempty"`
}

type EmitArtifactOutput struct {
	Name string `json:"name"`
}

type ConsumeArtifactInput struct {
	Artifact swf.ArtifactKey `json:"artifact" validate:"required"`
}

type ConsumeArtifactOutput struct {
	Name string `json:"name"`
}

type NextTaskOverrideInput struct {
	Message string `json:"message"`
}

type NextTaskOverrideOutput struct {
	Message string `json:"message"`
	Step    string `json:"step"`
}

func init() {
	// Register all test activities needed by the test suite
	registerTestActivities()
}

func registerTestActivities() {

	recipeops.Register(commandop.GetOp())
	recipeops.Register(sleepop.GetOp())

	// Register echo_activity
	echoActivity := recipeops.NewActivityMappedOpV2[GenericInput, GenericOutput](
		recipeops.OpMetadata{
			Type: "echo_activity",
		},
		func(_ recipeops.OpDependencies, ctx context.Context, input GenericInput) (GenericOutput, error) {
			message := input.Message
			if message == "" {
				message = "Hello, World!"
			}
			return GenericOutput{
				Output: message,
			}, nil
		},
	)
	recipeops.Register(echoActivity)

	// Register all other typed test activities
	registerTypedTestActivities()

	emitArtifact := recipeops.NewActivityMappedOpV2[struct{}, EmitArtifactOutput](
		recipeops.OpMetadata{Type: "test_emit_artifact"},
		func(deps recipeops.OpDependencies, ctx context.Context, _ struct{}) (EmitArtifactOutput, error) {
			artifact := swf.NewArtifactFromBytes("foo", []byte("hello world"))
			if err := deps.AddOutputArtifact(artifact); err != nil {
				return EmitArtifactOutput{}, err
			}
			return EmitArtifactOutput{Name: artifact.Name()}, nil
		},
	)
	recipeops.Register(emitArtifact)

	consumeArtifact := recipeops.NewActivityMappedOpV2[ConsumeArtifactInput, ConsumeArtifactOutput](
		recipeops.OpMetadata{Type: "test_consume_artifact"},
		func(_ recipeops.OpDependencies, ctx context.Context, input ConsumeArtifactInput) (ConsumeArtifactOutput, error) {
			return ConsumeArtifactOutput{Name: input.Artifact.Name}, nil
		},
	)
	recipeops.Register(consumeArtifact)

	nextTaskOverride := recipeops.NewOp().
		WithType("test_next_task_override").
		WithDescription("overrides the next task type from within an op").
		WithVersion("1.0.0").
		AddStep("first", recipeops.NewStepWithDeps(func(deps recipeops.OpDependencies, ctx context.Context, input NextTaskOverrideInput) (NextTaskOverrideOutput, error) {
			_ = ctx
			deps.SetNextTaskType("")
			return NextTaskOverrideOutput{
				Message: input.Message,
				Step:    "first",
			}, nil
		})).
		AddStep("second", recipeops.NewStepWithDeps(func(_ recipeops.OpDependencies, ctx context.Context, input NextTaskOverrideOutput) (NextTaskOverrideOutput, error) {
			_ = ctx
			_ = input
			return NextTaskOverrideOutput{}, nil
		})).
		BuildOrPanic()
	recipeops.Register(nextTaskOverride)
}
