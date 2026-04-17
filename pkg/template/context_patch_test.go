package template

import (
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/stretchr/testify/require"
)

func TestApplyContextPatch_JobPatch_PropagatesToAncestorsAndNewChildren(t *testing.T) {
	commit := &contextual.GitCommitContext{}
	job := contextual.JobContext{
		GitBase: contextual.GitBaseContext{
			BaseRepo:  "repo",
			BaseRef:   "main",
			GitAuthor: "old",
		},
	}

	root, err := NewRecipeResolutionContext(commit, map[string]any{}, job)
	require.NoError(t, err)

	seqMeta := recipe.NodeMetadata{ID: "seq1", Inputs: map[string]any{}}
	seq, err := root.NewChildContext(ScopeSequence, seqMeta, "", map[string]any{})
	require.NoError(t, err)

	opMeta := recipe.NodeMetadata{ID: "op1"}
	op, err := seq.NewChildContext(ScopeOp, opMeta, "op", nil)
	require.NoError(t, err)

	require.Equal(t, "old", root.TemplateData.Context.GitTask.GitAuthor)
	require.Equal(t, "old", seq.TemplateData.Context.GitTask.GitAuthor)
	require.Equal(t, "old", op.TemplateData.Context.GitTask.GitAuthor)

	patch := coretask.ContextPatch{
		Job: map[string]any{"git": map[string]any{"author": "new"}},
	}
	require.NoError(t, op.ApplyContextPatch(patch))

	require.Equal(t, "new", root.TemplateData.Context.GitTask.GitAuthor)
	require.Equal(t, "new", seq.TemplateData.Context.GitTask.GitAuthor)
	require.Equal(t, "new", op.TemplateData.Context.GitTask.GitAuthor)

	// New children should inherit patched job context from their parent.
	op2Meta := recipe.NodeMetadata{ID: "op2"}
	op2, err := seq.NewChildContext(ScopeOp, op2Meta, "op", nil)
	require.NoError(t, err)
	require.Equal(t, "new", op2.TemplateData.Context.GitTask.GitAuthor)
}

func TestApplyContextPatch_ScopedPatch_OnlyTouchesLocalContainers(t *testing.T) {
	commit := &contextual.GitCommitContext{}
	job := contextual.JobContext{}

	root, err := NewRecipeResolutionContext(commit, map[string]any{}, job)
	require.NoError(t, err)

	seq1Meta := recipe.NodeMetadata{ID: "seq1", Inputs: map[string]any{}}
	seq1, err := root.NewChildContext(ScopeSequence, seq1Meta, "", map[string]any{})
	require.NoError(t, err)

	seq2Meta := recipe.NodeMetadata{ID: "seq2", Inputs: map[string]any{}}
	seq2, err := root.NewChildContext(ScopeSequence, seq2Meta, "", map[string]any{})
	require.NoError(t, err)

	// seq1 has a step output; seq2 should be independent.
	seq1.TemplateData.Sequence["foo"] = StepOutput{Outputs: map[string]any{"myoutput": "old"}}

	opMeta := recipe.NodeMetadata{ID: "op1"}
	op, err := seq1.NewChildContext(ScopeOp, opMeta, "op", nil)
	require.NoError(t, err)

	patch := coretask.ContextPatch{
		Scopes: []coretask.ScopePatch{{
			Container: "sequence",
			ID:        "foo",
			Outputs:   map[string]any{"myoutput": "new"},
		}},
	}
	require.NoError(t, op.ApplyContextPatch(patch))

	require.Equal(t, "new", seq1.TemplateData.Sequence["foo"].Outputs["myoutput"])
	require.Empty(t, root.TemplateData.Sequence, "root should not share the sequence container for child sequences")
	require.Empty(t, seq2.TemplateData.Sequence, "sibling sequence should not see seq1 local patch")
}
