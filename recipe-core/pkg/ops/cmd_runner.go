package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/colony-2/swf-go/pkg/swf"
)

type commandExecutionEnvelope struct {
	Output       map[string]interface{} `json:"output,omitempty"`
	ArtifactRefs interface{}            `json:"artifact_refs,omitempty"`
}

// CommandMain executes a single-step RegisterableOp using the extension process contract.
func CommandMain(op RegisterableOp) {
	os.Exit(RunAsCommand(op))
}

// RunAsCommand executes a single-step RegisterableOp using JSON stdin/stdout and env-provided context.
func RunAsCommand(op RegisterableOp) int {
	if op == nil {
		_, _ = fmt.Fprintln(os.Stderr, "op is required")
		return 1
	}
	steps := op.TaskChain()
	if len(steps) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "op %q has no task steps\n", op.GetName())
		return 1
	}

	input := map[string]interface{}{}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil && err.Error() != "EOF" {
		_, _ = fmt.Fprintf(os.Stderr, "decode input: %v\n", err)
		return 1
	}

	deps := NewOpDependenciesBuilder().
		WithWorktreePath(strings.TrimSpace(os.Getenv("VIBETHIS_WORKTREE_PATH"))).
		WithGitContext(loadCommandGitContext()).
		Build()

	output, err := steps[0].Invoke(deps, context.Background(), input)
	writeCommandArtifacts(deps.GetOutputArtifacts(), commandOutboxPath(input))

	envelope := commandExecutionEnvelope{Output: output}
	if refs := deps.GetExternalArtifacts(); len(refs) > 0 {
		envelope.ArtifactRefs = refs
	}

	if err == nil {
		if encodeErr := json.NewEncoder(os.Stdout).Encode(envelope); encodeErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "encode output: %v\n", encodeErr)
			return 1
		}
		return 0
	}

	if len(output) > 0 || envelope.ArtifactRefs != nil {
		_ = json.NewEncoder(os.Stdout).Encode(envelope)
	}
	_, _ = fmt.Fprintln(os.Stderr, err.Error())
	return 1
}

func loadCommandGitContext() GitExecutionContext {
	ctx := GitExecutionContext{
		BaseRepo:         strings.TrimSpace(os.Getenv("VIBETHIS_GIT_BASE_REPO")),
		BaseRef:          strings.TrimSpace(os.Getenv("VIBETHIS_GIT_BASE_REF")),
		ResolvedBaseHash: strings.TrimSpace(os.Getenv("VIBETHIS_GIT_RESOLVED_BASE_HASH")),
		RecipeSourceRepo: strings.TrimSpace(os.Getenv("VIBETHIS_GIT_RECIPE_SOURCE_REPO")),
		RecipeSourceRef:  strings.TrimSpace(os.Getenv("VIBETHIS_GIT_RECIPE_SOURCE_REF")),
		PersistHash:      strings.TrimSpace(os.Getenv("VIBETHIS_GIT_PERSIST_HASH")),
		ParentHash:       strings.TrimSpace(os.Getenv("VIBETHIS_GIT_PARENT_HASH")),
		CellName:         strings.TrimSpace(os.Getenv("VIBETHIS_GIT_CELL_NAME")),
		CellPath:         strings.TrimSpace(os.Getenv("VIBETHIS_GIT_CELL_PATH")),
		NodePath:         strings.TrimSpace(os.Getenv("VIBETHIS_GIT_NODE_PATH")),
		InvokeHash:       strings.TrimSpace(os.Getenv("VIBETHIS_GIT_INVOKE_HASH")),
		WorktreePath:     strings.TrimSpace(os.Getenv("VIBETHIS_WORKTREE_PATH")),
	}
	if invokeSeq := strings.TrimSpace(os.Getenv("VIBETHIS_GIT_INVOKE_SEQ")); invokeSeq != "" {
		if parsed, err := strconv.ParseInt(invokeSeq, 10, 64); err == nil {
			ctx.InvokeSeq = parsed
		}
	}
	return ctx
}

func commandOutboxPath(input map[string]interface{}) string {
	if outbox, ok := input["artifact_outbox_path"].(string); ok && strings.TrimSpace(outbox) != "" {
		return strings.TrimSpace(outbox)
	}
	return ""
}

func writeCommandArtifacts(artifacts []swf.Artifact, outbox string) {
	if len(artifacts) == 0 || strings.TrimSpace(outbox) == "" {
		return
	}
	for _, artifact := range artifacts {
		dest := filepath.Join(outbox, artifact.Name())
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			continue
		}
		_ = artifact.SaveToFile(context.Background(), dest)
	}
}
