package input

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/google/uuid"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Actor is supplied by the embedding application, not taken from response data.
// The application is responsible for authenticating this identity.
type Actor struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type Receipt struct {
	RequestID    string    `json:"request_id"`
	SubmissionID string    `json:"submission_id"`
	SubmittedAt  time.Time `json:"submitted_at"`
	Actor        Actor     `json:"actor"`
}

// StructuredSubmission accepts ordinary JSON values and stored artifact refs.
// Go callers can also place jobdb.Artifact values in Response: these are attached
// to the outcome and replaced by stored refs before schema validation. Their
// contents never become inline JSON. ArtifactRefs provides explicit named bindings.
type StructuredSubmission struct {
	RequestID    string                         `json:"request_id"`
	SubmissionID string                         `json:"submission_id"`
	Response     any                            `json:"response"`
	ArtifactRefs map[string]recipeartifacts.Ref `json:"artifact_refs,omitempty"`
}

type localSchemasOnly struct{}

func (localSchemasOnly) Load(url string) (any, error) {
	return nil, fmt.Errorf("schema references must be bundled with the input request: %s", url)
}

func compileSchema(schema map[string]any) (*jsonschema.Schema, error) {
	value, err := jsonValue(schema)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(localSchemasOnly{})
	const resource = "urn:c2j:input-schema"
	if err := c.AddResource(resource, value); err != nil {
		return nil, err
	}
	return c.Compile(resource)
}

func validateSchema(schema map[string]any, value any) error {
	c, err := compileSchema(schema)
	if err != nil {
		return err
	}
	v, err := jsonValue(value)
	if err != nil {
		return err
	}
	return c.Validate(v)
}

func jsonValue(value any) (any, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out any
	err = json.Unmarshal(b, &out)
	return out, err
}

func buildStructuredForm(deps ops.OpDependencies, ctx context.Context, config Config) (InputForm, error) {
	if _, err := compileSchema(config.ResponseSchema); err != nil {
		return InputForm{}, ValidationError{Field: "form.response_schema", Message: err.Error()}
	}
	b := newArtifactBinder(ctx, deps, nil)
	transferred := false
	defer func() {
		if !transferred {
			b.cleanup()
		}
	}()
	request, err := b.bind(config.Request)
	if err != nil {
		return InputForm{}, ValidationError{Field: "form.request", Message: err.Error()}
	}
	if config.RequestSchema != nil {
		if err := validateSchema(config.RequestSchema, request); err != nil {
			return InputForm{}, ValidationError{Field: "form.request", Message: err.Error()}
		}
	}
	form := InputForm{RequestedAt: time.Now().UTC().Format(time.RFC3339Nano), RequestID: uuid.NewString(), Request: request, RequestSchema: config.RequestSchema,
		ResponseSchema: config.ResponseSchema, Presentation: config.Presentation, Title: config.Title}
	// Snapshot mutable caller-owned maps along with the request.
	raw, err := json.Marshal(form)
	if err != nil {
		return InputForm{}, err
	}
	form = InputForm{}
	if err := json.Unmarshal(raw, &form); err != nil {
		return InputForm{}, err
	}
	for _, artifact := range b.artifacts {
		if err := deps.AddOutputArtifact(artifact); err != nil {
			return InputForm{}, err
		}
	}
	if config.Output != nil {
		form.Output = config.Output
		deps.SetNextTaskType(autoFillTaskType)
	}
	transferred = true
	return form, nil
}

// GetForm returns the frozen form independently of any presentation transport.
func (r *Runtime) GetForm(ctx context.Context, projectID, jobID string) (InputForm, error) {
	_, req, _, err := r.getOutput(ctx, projectID, jobID)
	if err != nil {
		return InputForm{}, err
	}
	var form InputForm
	err = ops.DecodeWithJsonTags(req.OpOutput, &form)
	if err != nil {
		return InputForm{}, invalidFormError(err)
	}
	return form, nil
}

// SubmitStructuredResponse validates against the recorded request before finishing
// the exact waiting task. Successful return is the same output recorded for the op.
// Recovery of ambiguous completion failures remains JobDB's responsibility.
func (r *Runtime) SubmitStructuredResponse(ctx context.Context, projectID, jobID string, submission StructuredSubmission, actor Actor) (Output, error) {
	return r.submitInput(ctx, projectID, jobID, func(form InputForm, b *artifactBinder) (Output, error) {
		return acceptStructured(form, submission, actor, b)
	})
}

// submitInput preserves the captured waiting task and its execution context.
func (r *Runtime) submitInput(ctx context.Context, projectID, jobID string, accept func(InputForm, *artifactBinder) (Output, error)) (Output, error) {
	task, req, artifacts, err := r.getOutput(ctx, projectID, jobID)
	if err != nil {
		return Output{}, err
	}
	var form InputForm
	if err := ops.DecodeWithJsonTags(req.OpOutput, &form); err != nil {
		return Output{}, invalidFormError(err)
	}
	b := &artifactBinder{ctx: ctx, job: jobdb.JobKey{TenantId: projectID, JobId: jobID}, ordinal: task.TaskOrdinalToComplete(),
		refs: map[string]recipeartifacts.Ref{}, reserved: map[string]bool{}, resolve: func(key jobdb.ArtifactKey) jobdb.Artifact {
			return r.ctl.GetArtifactLazy(ctx, projectID, key)
		}}
	defer b.cleanup()
	for _, a := range artifacts {
		b.reserved[a.Name()] = true
	}
	out, err := accept(form, b)
	if err != nil {
		return Output{}, err
	}
	value, err := jsonValue(out)
	if err != nil {
		return Output{}, runtimeError(ErrStorage, "encode input outcome", err)
	}
	// Retain the workspace, git snapshot, execution requirements and job context.
	req.OpOutput = value.(map[string]any)
	req.NextTask = ""
	req.NextTaskAlternate = nil
	if req.ArtifactRefs == nil {
		req.ArtifactRefs = map[string]recipeartifacts.Ref{}
	}
	for name, ref := range out.ArtifactRefs {
		req.ArtifactRefs[name] = ref
	}
	env, err := coretask.NewOutputEnvelope(coretask.OutputKindActivityInvocationOutput, req)
	if err != nil {
		return Output{}, runtimeError(ErrStorage, "encode input outcome", err)
	}
	data, err := jobdb.NewTaskData(env, append(artifacts, b.artifacts...)...)
	if err != nil {
		return Output{}, runtimeError(ErrStorage, "prepare input outcome", err)
	}
	if err := r.finishInput(ctx, task, data); err != nil {
		return Output{}, err
	}
	if r.sse != nil {
		r.sse.Broadcast(ops.SSEEvent{Type: "input_completed", Data: map[string]interface{}{"jobId": jobID}})
	}
	return out, nil
}

func acceptStructured(form InputForm, submission StructuredSubmission, actor Actor, b *artifactBinder) (Output, error) {
	bad := func(field, message string) (Output, error) {
		return Output{}, ValidationError{Field: field, Message: message}
	}
	if form.ResponseSchema == nil {
		return bad("response_schema", "input is not structured")
	}
	if form.RequestID == "" || submission.RequestID != form.RequestID {
		return Output{}, runtimeError(ErrStaleRequest, "submit structured input", nil)
	}
	if strings.TrimSpace(submission.SubmissionID) == "" {
		return bad("submission_id", "is required")
	}
	if strings.TrimSpace(actor.ID) == "" || strings.TrimSpace(actor.Kind) == "" {
		return bad("actor", "id and kind are required")
	}
	for name, ref := range submission.ArtifactRefs {
		if err := b.checkRef(ref); err != nil {
			return Output{}, inputFieldError("artifact_refs."+name, err)
		}
		if err := b.addRef(name, ref); err != nil {
			return Output{}, inputFieldError("artifact_refs."+name, err)
		}
	}
	response, err := b.bind(submission.Response)
	if err != nil {
		return Output{}, inputFieldError("response", err)
	}
	if err := validateSchema(form.ResponseSchema, response); err != nil {
		return bad("response", err.Error())
	}
	return Output{Response: response, ArtifactRefs: b.refs, Receipt: &Receipt{
		RequestID: form.RequestID, SubmissionID: submission.SubmissionID, SubmittedAt: time.Now().UTC(), Actor: actor,
	}}, nil
}

// Preserve an explicitly empty schema (accept any JSON) across task persistence.
func (f InputForm) MarshalJSON() ([]byte, error) {
	type plain InputForm
	var responseSchema *map[string]any
	if f.ResponseSchema != nil {
		responseSchema = &f.ResponseSchema
	}
	return json.Marshal(struct {
		plain
		ResponseSchema *map[string]any `json:"response_schema,omitempty"`
	}{plain: plain(f), ResponseSchema: responseSchema})
}

func (c Config) MarshalJSON() ([]byte, error) {
	type plain Config
	var responseSchema *map[string]any
	if c.ResponseSchema != nil {
		responseSchema = &c.ResponseSchema
	}
	return json.Marshal(struct {
		plain
		ResponseSchema *map[string]any `json:"response_schema,omitempty"`
	}{plain: plain(c), ResponseSchema: responseSchema})
}
