package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/process"
)

const ExecutionOpType = "extension_execution"
const extensionSandboxMount = "/extension"

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
	base := ops.NewActivityMappedOpV2[ExecutionInput, map[string]interface{}](
		ops.OpMetadata{
			Type:             ExecutionOpType,
			Description:      "Executes a selector-backed extension op",
			Version:          "1.0.0",
			DefaultTimeout:   30 * time.Minute,
			AcceptsArtifacts: true,
		},
		executeExtension,
	)
	return executionOp{RegisterableOp: base}
}

type executionOp struct {
	ops.RegisterableOp
}

func (o executionOp) TransformOperationPaths(ctx context.Context, req ops.OperationPathTransformRequest) (ops.OperationPathTransformResult, error) {
	return process.TransformOperationPaths(ctx, extensionSandboxInput(req.Input), req.Host)
}

func extensionSandboxInput(input map[string]interface{}) interface{} {
	rawInputs, ok := input["inputs"]
	if !ok {
		return nil
	}
	inputs, ok := rawInputs.(map[string]interface{})
	if !ok {
		return nil
	}
	return inputs["sandbox"]
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
	payload, sandbox, err := resolved.SanitizeInvocationInputs(input.Inputs)
	if err != nil {
		return nil, err
	}
	if err := resolved.ValidateInvocationInputs(input.Inputs); err != nil {
		return nil, fmt.Errorf("extension input validation failed: %w", err)
	}

	inJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal extension input: %w", err)
	}
	env := buildExecutionEnv(resolved)

	var cancel context.CancelFunc
	if d, err := parseDurationOrZero(resolved.Spec.Timeout); err == nil && d > 0 {
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	runReq, err := extensionRunRequest(deps, resolved, sandbox, env, inJSON)
	if err != nil {
		return nil, err
	}
	stdout, stderr, err := process.ExecuteProcess(ctx, runReq)
	if err != nil {
		return nil, fmt.Errorf("extension op %q failed: %w; stderr: %s", input.Selector, err, strings.TrimSpace(string(stderr)))
	}

	outputs, artifactRefs, err := decodeExecutionEnvelope(stdout)
	if err != nil {
		return nil, fmt.Errorf("extension op %q produced invalid JSON on stdout: %w; raw: %s", input.Selector, err, strings.TrimSpace(string(stdout)))
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

func extensionRunRequest(deps ops.OpDependencies, resolved *ResolvedOp, sandbox *SandboxInput, env map[string]string, stdin []byte) (process.RunRequest, error) {
	req := process.RunRequest{
		WorkspaceRoot: resolved.ProjectRoot,
		WorkingDir:    resolved.WorkingDir(),
		ConfigFile:    filepath.Join(resolved.ProjectRoot, ".shai", "config.yaml"),
		Shell:         resolved.Spec.Shell,
		Run:           resolved.Spec.Run,
		Command:       resolved.Spec.Command,
		Env:           env,
		Stdin:         stdin,
		Sandbox:       sandbox,
	}
	if process.SandboxType(sandbox) != process.SandboxTypeShai {
		return req, nil
	}
	runtimeProvider, ok := deps.(ops.OperationPathRuntimeProvider)
	if !ok {
		return req, nil
	}
	pathRuntime := runtimeProvider.OperationPathRuntime()
	if strings.TrimSpace(pathRuntime.Views.Host.Workdir) == "" {
		return req, nil
	}
	extensionWorkingDir, err := extensionSandboxWorkingDir(resolved.ProjectRoot, resolved.WorkingDir())
	if err != nil {
		return process.RunRequest{}, err
	}
	req.WorkspaceRoot = pathRuntime.Views.Host.Workdir
	req.WorkingDir = extensionWorkingDir
	req.RequiredMounts = append([]ops.RequiredMount{}, pathRuntime.Mounts...)
	req.RequiredMounts = append(req.RequiredMounts, ops.RequiredMount{
		Source: resolved.ProjectRoot,
		Target: extensionSandboxMount,
		Mode:   ops.MountModeReadWrite,
	})
	return req, nil
}

func extensionSandboxWorkingDir(projectRoot string, workingDir string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	workingDir = strings.TrimSpace(workingDir)
	if projectRoot == "" || workingDir == "" {
		return extensionSandboxMount, nil
	}
	rel, err := filepath.Rel(projectRoot, workingDir)
	if err != nil {
		return "", fmt.Errorf("extension working directory: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("extension working directory %q escapes project root %q", workingDir, projectRoot)
	}
	if rel == "." || rel == "" {
		return extensionSandboxMount, nil
	}
	return path.Join(extensionSandboxMount, filepath.ToSlash(rel)), nil
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
