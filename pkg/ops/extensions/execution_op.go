package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/colony-2/c2j/pkg/ops/executiondiag"
	"os/exec"
	"strings"

	"github.com/colony-2/c2j/internal/preparedtest"
	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const ExecutionOpType = "extension_execution"

type ExecutionInput struct {
	Env              map[string]string      `json:"env,omitempty" secret:"env"`
	Selector         string                 `json:"selector" validate:"required"`
	Inputs           map[string]interface{} `json:"inputs,omitempty" secret:"schema"`
	RepositorySource string                 `json:"repository_source,omitempty"`
	RepositoryRef    string                 `json:"repository_ref,omitempty"`
}

type executionEnvelope struct {
	Output       map[string]interface{}         `json:"output,omitempty"`
	ArtifactRefs map[string]recipeartifacts.Ref `json:"artifact_refs,omitempty"`
}

func GetExecutionOp() ops.RegisterableOp {
	return ops.NewActivityMappedOpV2[ExecutionInput, map[string]interface{}](
		ops.OpMetadata{
			Type:             ExecutionOpType,
			Description:      "Executes a selector-backed extension op",
			Version:          "1.0.0",
			AcceptsArtifacts: true,
		},
		executeExtension,
	)
}

func executeExtension(deps ops.OpDependencies, ctx context.Context, input ExecutionInput) (result map[string]interface{}, runErr error) {
	report := executiondiag.New(ctx, deps)
	defer report.Close()
	report.Selector = input.Selector
	defer func() {
		if execution.IsRequired(runErr) {
			return
		}
		exit := 0
		if !report.Capture.Launched || report.Capture.Err != nil {
			exit = -1
			var ee *exec.ExitError
			if errors.As(report.Capture.Err, &ee) {
				exit = ee.ExitCode()
			}
		}
		timedOut := errors.Is(report.Capture.Err, context.DeadlineExceeded)
		var native *jobdb.TimeoutError
		timedOut = timedOut || errors.As(report.Capture.Err, &native)
		details := report.Publish(runErr, exit, timedOut)
		if runErr != nil {
			message := executiondiag.Short(secrets.RedactorFromContext(ctx).Redact(runErr.Error()))
			if len(details.Diagnostics) > 0 {
				message += "; execution log: " + report.Name
			} else {
				message += "; execution log unavailable"
			}
			runErr = &executiondiag.Failure{Cause: runErr, Message: message, Attribute: "extension", Details: details}
		}
	}()
	gitCtx := deps.GitContext()
	repoSource := strings.TrimSpace(input.RepositorySource)
	repoRef := strings.TrimSpace(input.RepositoryRef)
	if repoSource == "" {
		repoSource = strings.TrimSpace(gitCtx.RecipeSourceRepo)
	}
	if repoRef == "" {
		repoRef = strings.TrimSpace(gitCtx.RecipeSourceRef)
	}
	resolved, _ := ctx.Value(preparedOpKey{}).(*ResolvedOp)
	var err error
	if resolved == nil {
		if IsNixSelector(input.Selector) {
			return nil, fmt.Errorf("invoke a Nix extension directly with op: %s so c2j can prepare it before execution", input.Selector)
		}
		resolved, err = Resolve(ctx, input.Selector, ResolveOptions{BaseDir: deps.WorktreePath(), RepositorySource: repoSource, RepositoryRef: repoRef})
		if err == nil {
			// The lower-level execution op has no task-time manifest preparation.
			// Never silently omit a default whose provider was not consulted.
			defaults := map[string]any{}
			if _, err := resolved.ApplyInvocationDefaults(defaults); err != nil {
				return nil, err
			}
			names, err := secrets.Names(defaults)
			if err != nil {
				return nil, err
			}
			if len(names) > 0 || configuration.Contains(defaults) {
				return nil, fmt.Errorf("invoke an extension with reference defaults directly with op: %s so c2j can resolve them during task preparation", input.Selector)
			}
		}
	} else {
		resolved, err = RestoreResolvedOp(resolved)
	}
	if err != nil {
		return nil, err
	}
	report.ResolvedSelector, report.ResolvedCommit = resolved.ResolvedSelector, resolved.ResolvedCommit
	report.Roots = append(report.Roots, executiondiag.Root{Name: "extension_source", Path: resolved.ProjectRoot})
	report.Capture.Request = extensionRunRequest(resolved, nil, nil)
	if input.Inputs == nil {
		input.Inputs = map[string]interface{}{}
	}
	defaulted, err := secrets.ApplySchemaDefaults(resolved.Spec.InputSchema, input.Inputs)
	if err != nil {
		return nil, err
	}
	input.Inputs = defaulted.(map[string]any)
	if err := resolved.ValidateInvocationInputs(input.Inputs); err != nil {
		return nil, fmt.Errorf("extension input validation failed: %w", err)
	}

	materialized, err := secrets.FromContext(ctx).Materialize(input.Inputs)
	if err != nil {
		return nil, err
	}
	input.Inputs, _ = materialized.(map[string]interface{})
	if err := resolved.ValidateMaterializedInputs(input.Inputs); err != nil {
		return nil, fmt.Errorf("extension input validation failed: %w", err)
	}
	payload, err := hydrateObjects(ctx, deps, input.Inputs)
	if err != nil {
		return nil, fmt.Errorf("restore extension objects: %w", err)
	}
	inJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal extension input: %w", err)
	}
	env := buildExecutionEnv(resolved)
	for k, v := range input.Env {
		env[k] = v
	}
	env = jobcontext.MergeProtectedEnv(env, deps.ProtectedEnv())
	allocation := execution.CurrentEnvironment(ctx)
	allocationJSON, err := json.Marshal(allocation)
	if err != nil {
		return nil, err
	}
	env[execution.AllocationEnv] = string(allocationJSON)
	if err := prepareObjectOutbox(deps, env); err != nil {
		return nil, err
	}

	var cancel context.CancelFunc
	if d, err := parseDurationOrZero(resolved.Spec.Timeout); err == nil && d > 0 {
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	runReq := extensionRunRequest(resolved, env, inJSON)
	runReq = report.Prepare(runReq)
	stdout, scripted, err := preparedtest.Complete(ctx, preparedtest.Invocation{
		Selector: input.Selector, ResolvedSelector: resolved.ResolvedSelector,
		Inputs: payload, Env: env,
	})
	if !scripted {
		if err != nil {
			return nil, fmt.Errorf("prepared completion selection: %w", err)
		}
		stdout, _, err = process.ExecuteProcess(ctx, runReq)
	} else {
		// A scripted completion never claims to have launched a process. Keep
		// normal protocol decoding/publication below, including bounded output.
		report.Capture.Err = err
		if len(stdout) > process.CaptureLimit {
			return nil, fmt.Errorf("scripted extension stdout exceeded execution capture limit")
		}
	}
	var exit *exec.ExitError
	if ctx.Err() == nil && errors.As(err, &exit) && exit.ExitCode() == executionRequiredExit {
		if report.Capture.Truncated(0) {
			return nil, fmt.Errorf("extension execution_required response exceeded execution capture limit")
		}
		needs, decodeErr := decodeExecutionRequired(report.Capture.RawBytes(0))
		if decodeErr != nil {
			return nil, fmt.Errorf("invalid extension execution_required response: %w", decodeErr)
		}
		if err := secrets.FromContext(ctx).CheckOutput(needs); err != nil {
			return nil, err
		}
		mismatches, err := execution.Compare(needs, allocation.Allocation)
		if err != nil {
			return nil, err
		}
		if len(mismatches) == 0 {
			return nil, fmt.Errorf("extension execution_required protocol error: requested requirements are already satisfied")
		}
		return nil, &execution.Required{Needs: needs}
	}
	if err != nil {
		return nil, fmt.Errorf("extension op %q failed: %w; stderr: %s", input.Selector, err, executiondiag.Short(strings.TrimSpace(string(report.Capture.Bytes(1)))))
	}

	if report.Capture.Truncated(0) {
		return nil, fmt.Errorf("extension stdout exceeded execution capture limit; JSON result was not decoded")
	}
	outputs, artifactRefs, err := decodeExecutionEnvelope(stdout)
	if err != nil {
		return nil, fmt.Errorf("extension op %q produced invalid JSON on stdout: %w; raw: %s", input.Selector, err, executiondiag.Short(secrets.RedactorFromContext(ctx).Redact(strings.TrimSpace(string(stdout)))))
	}
	if err := executiondiag.CheckOutput(executionEnvelope{Output: outputs, ArtifactRefs: artifactRefs}); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(stdout)) > 0 {
		outputs, err = publishExtensionObjects(ctx, deps, stdout, outputs)
		if err != nil {
			return nil, fmt.Errorf("extension object output: %w", err)
		}
	}
	if resolved.compiledOutput != nil {
		if err := resolved.compiledOutput.Validate(outputs); err != nil {
			return nil, fmt.Errorf("extension op %q output failed schema validation: %w", input.Selector, err)
		}
	}
	if err := executiondiag.CheckOutput(executionEnvelope{Output: outputs, ArtifactRefs: artifactRefs}); err != nil {
		return nil, err
	}
	for name, ref := range artifactRefs {
		if ref.External == nil {
			continue
		}
		if err := deps.AddExternalArtifact(name, ref.External.URL, ref.External.Expand); err != nil {
			return nil, err
		}
	}
	return outputs, nil
}

func extensionRunRequest(resolved *ResolvedOp, env map[string]string, stdin []byte) process.RunRequest {
	return process.RunRequest{
		WorkspaceRoot: resolved.ProjectRoot,
		WorkingDir:    resolved.WorkingDir(),
		Shell:         resolved.Spec.Shell,
		Run:           resolved.Spec.Run,
		Command:       resolved.Spec.Command,
		Env:           env,
		Stdin:         stdin,
	}
}

func buildExecutionEnv(resolved *ResolvedOp) map[string]string {
	env := map[string]string{}
	for key, value := range resolved.Spec.Env {
		env[key] = value
	}
	return env
}

func decodeExecutionEnvelope(stdout []byte) (map[string]interface{}, map[string]recipeartifacts.Ref, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return map[string]interface{}{}, nil, nil
	}
	raw := map[string]interface{}{}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, nil, err
	}

	outputs := raw
	if wrapped, ok := raw["output"].(map[string]interface{}); ok {
		outputs = wrapped
	}

	artifactRefs := map[string]recipeartifacts.Ref{}
	if wrapped, ok := raw["artifact_refs"]; ok {
		buf, err := json.Marshal(wrapped)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(buf, &artifactRefs); err != nil {
			return nil, nil, err
		}
	}
	return outputs, artifactRefs, nil
}

type preparedOpKey struct{}

// WithPreparedOp supplies a manifest/source prepared before the task deadline.
func WithPreparedOp(ctx context.Context, op *ResolvedOp) context.Context {
	return context.WithValue(ctx, preparedOpKey{}, op)
}
