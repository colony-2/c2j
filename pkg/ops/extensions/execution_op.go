package extensions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
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
	env := buildExecutionEnv(deps, resolved, inJSON)

	var cancel context.CancelFunc
	if d, err := parseDurationOrZero(resolved.Spec.Timeout); err == nil && d > 0 {
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	stdout, stderr, err := ExecuteProcess(ctx, RunRequest{
		WorkspaceRoot: resolved.ProjectRoot,
		WorkingDir:    resolved.WorkingDir(),
		Shell:         resolved.Spec.Shell,
		Run:           resolved.Spec.Run,
		Command:       resolved.Spec.Command,
		Args:          resolved.Spec.Args,
		Env:           env,
		Stdin:         inJSON,
		Sandbox:       sandbox,
	})
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

func buildExecutionEnv(deps ops.OpDependencies, resolved *ResolvedOp, inputJSON []byte) map[string]string {
	env := map[string]string{}
	for key, value := range resolved.Spec.Env {
		env[key] = value
	}
	gitCtx := deps.GitContext()
	env["VIBETHIS_PROJECT_ROOT"] = resolved.ProjectRoot
	env["VIBETHIS_OP_DIR"] = resolved.OpDir
	env["VIBETHIS_OP_NAME"] = resolved.Spec.Name
	env["VIBETHIS_OP_SELECTOR"] = resolved.Selector
	env["VIBETHIS_INPUT_JSON"] = string(inputJSON)
	env["VIBETHIS_WORKTREE_PATH"] = deps.WorktreePath()
	env["VIBETHIS_GIT_BASE_REPO"] = gitCtx.BaseRepo
	env["VIBETHIS_GIT_BASE_REF"] = gitCtx.BaseRef
	env["VIBETHIS_GIT_RESOLVED_BASE_HASH"] = gitCtx.ResolvedBaseHash
	env["VIBETHIS_GIT_RECIPE_SOURCE_REPO"] = gitCtx.RecipeSourceRepo
	env["VIBETHIS_GIT_RECIPE_SOURCE_REF"] = gitCtx.RecipeSourceRef
	env["VIBETHIS_GIT_PERSIST_HASH"] = gitCtx.PersistHash
	env["VIBETHIS_GIT_PARENT_HASH"] = gitCtx.ParentHash
	env["VIBETHIS_GIT_CELL_NAME"] = gitCtx.CellName
	env["VIBETHIS_GIT_CELL_PATH"] = gitCtx.CellPath
	env["VIBETHIS_GIT_NODE_PATH"] = gitCtx.NodePath
	env["VIBETHIS_GIT_INVOKE_HASH"] = gitCtx.InvokeHash
	if gitCtx.InvokeSeq != 0 {
		env["VIBETHIS_GIT_INVOKE_SEQ"] = strconv.FormatInt(gitCtx.InvokeSeq, 10)
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
