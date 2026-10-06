package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
)

const ExecutionOpType = "extension_execution"

type ExecutionInput struct {
	Selector         string                 `json:"selector" validate:"required"`
	Inputs           map[string]interface{} `json:"inputs,omitempty"`
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
			DefaultTimeout:   30 * time.Minute,
			AcceptsArtifacts: true,
		},
		executeExtension,
	)
}

func executeExtension(deps ops.OpDependencies, ctx context.Context, input ExecutionInput) (map[string]interface{}, error) {
	gitCtx := deps.GitContext()
	repoSource := strings.TrimSpace(input.RepositorySource)
	repoRef := strings.TrimSpace(input.RepositoryRef)
	if repoSource == "" {
		repoSource = strings.TrimSpace(gitCtx.RecipeSourceRepo)
	}
	if repoRef == "" {
		repoRef = strings.TrimSpace(gitCtx.RecipeSourceRef)
	}
	resolved, err := Resolve(ctx, input.Selector, ResolveOptions{
		BaseDir:          deps.WorktreePath(),
		RepositorySource: repoSource,
		RepositoryRef:    repoRef,
	})
	if err != nil {
		return nil, err
	}
	if input.Inputs == nil {
		input.Inputs = map[string]interface{}{}
	}
	if err := resolved.ValidateInvocationInputs(input.Inputs); err != nil {
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
	env = jobcontext.MergeProtectedEnv(env, deps.ProtectedEnv())
	if err := prepareObjectOutbox(deps, env); err != nil {
		return nil, err
	}

	var cancel context.CancelFunc
	if d, err := parseDurationOrZero(resolved.Spec.Timeout); err == nil && d > 0 {
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	runReq := extensionRunRequest(resolved, env, inJSON)
	stdout, stderr, err := process.ExecuteProcess(ctx, runReq)
	if err != nil {
		return nil, fmt.Errorf("extension op %q failed: %w; stderr: %s", input.Selector, err, strings.TrimSpace(string(stderr)))
	}

	outputs, artifactRefs, err := decodeExecutionEnvelope(stdout)
	if err != nil {
		return nil, fmt.Errorf("extension op %q produced invalid JSON on stdout: %w; raw: %s", input.Selector, err, strings.TrimSpace(string(stdout)))
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
