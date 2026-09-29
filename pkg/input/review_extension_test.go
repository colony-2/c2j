package input

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestReviewExtensionGeneratesEnforcedResponseSchema(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	inbox := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(inbox, "documents"), 0700))
	original := []byte("# Design\r\nExact bytes: é\n")
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "documents", "design.md"), original, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "documents", "evidence.txt"), []byte("PASS"), 0600))
	design := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child-job", TaskOrdinal: 4, Name: "design.md", SizeBytes: int64(len(original))})
	evidence := recipeartifacts.NewStoredRef(jobdb.ArtifactKey{JobId: "child-job", TaskOrdinal: 5, Name: "evidence.txt", SizeBytes: 4})
	inputs := map[string]any{
		"inbox": inbox, "documents": map[string]any{"design": design, "evidence": evidence},
		"spec": map[string]any{"title": "Review design", "decisions": map[string]any{
			"approve": map[string]any{"label": "Approve", "accepts_reviewed_content": true},
			"revise":  map[string]any{"label": "Revise", "feedback_required": true},
		}, "document_options": map[string]any{"evidence": map[string]any{"media_type": "text/plain", "annotation_policy": "none"}}},
	}
	invoke := func() (map[string]any, error) {
		return extensions.GetExecutionOp().TaskChain()[0].Invoke(ops.NewOpDependenciesBuilder().WithWorktreePath(root).Build(), context.Background(), map[string]any{"selector": "./extensions/review", "inputs": inputs})
	}
	out, err := invoke()
	require.NoError(t, err)
	var config Config
	require.NoError(t, ops.DecodeWithJsonTags(out["form"].(map[string]any), &config))
	request := config.Request.(map[string]any)
	doc := request["documents"].(map[string]any)["design"].(map[string]any)
	digest := fmt.Sprintf("%x", sha256.Sum256(original))
	require.Equal(t, digest, doc["sha256"])
	ref, err := jsonValue(design)
	require.NoError(t, err)
	actualRef, err := jsonValue(doc["artifact"])
	require.NoError(t, err)
	require.Equal(t, ref, actualRef)
	annotation := func(hash string) any {
		return map[string]any{"base_sha256": hash, "format": "criticmarkup", "artifact": design}
	}
	for _, tc := range []struct {
		name     string
		response any
		valid    bool
	}{
		{"approve", map[string]any{"decision": "approve"}, true},
		{"feedback", map[string]any{"decision": "revise", "feedback": "Explain recovery"}, true},
		{"annotation", map[string]any{"decision": "revise", "annotations": map[string]any{"design": annotation(digest)}}, true},
		{"unknown decision", map[string]any{"decision": "merge"}, false},
		{"empty revision", map[string]any{"decision": "revise"}, false},
		{"whitespace feedback", map[string]any{"decision": "revise", "feedback": " \n\t"}, false},
		{"wrong hash", map[string]any{"decision": "revise", "annotations": map[string]any{"design": annotation("wrong")}}, false},
		{"approval annotations", map[string]any{"decision": "approve", "annotations": map[string]any{"design": annotation(digest)}}, false},
		{"unknown document", map[string]any{"decision": "revise", "annotations": map[string]any{"other": annotation(digest)}}, false},
		{"readonly document", map[string]any{"decision": "revise", "annotations": map[string]any{"evidence": annotation(digest)}}, false},
		{"inline content", map[string]any{"decision": "revise", "annotations": map[string]any{"design": map[string]any{"base_sha256": digest, "format": "criticmarkup", "markdown": "text"}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSchema(config.ResponseSchema, tc.response)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	require.NoError(t, os.Remove(filepath.Join(inbox, "documents", "evidence.txt")))
	_, err = invoke()
	require.ErrorContains(t, err, "review preparation failed")
}
