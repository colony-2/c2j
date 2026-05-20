package compiler

import (
	"strings"
	"testing"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
)

func newChildGroupRenderContext(t *testing.T, inputs map[string]interface{}, artifacts map[string]recipeartifacts.Ref) *template.ResolutionContext {
	t.Helper()
	jobCtx, gitCtx := GenerateTestContext()
	jobCtx.Artifacts = artifacts
	resCtx, err := template.NewRecipeResolutionContext(&gitCtx, inputs, jobCtx)
	if err != nil {
		t.Fatalf("new resolution context: %v", err)
	}
	return resCtx
}

func TestRenderChildGroupInputExpandsDynamicChildrenWithLocals(t *testing.T) {
	resCtx := newChildGroupRenderContext(t, map[string]interface{}{
		"targets": []interface{}{
			map[string]interface{}{"key": "first", "recipe": "child-a", "value": "alpha", "enabled": true},
			map[string]interface{}{"key": "second", "recipe": "child-b", "value": "beta", "enabled": false},
		},
	}, nil)

	out, err := renderChildGroupInput(resCtx, recipe.ChildGroupData{
		Mode:         "run_and_get_result",
		ChildrenFrom: "${{ inputs.targets }}",
		Child: &recipe.ChildGroupChild{
			Key:      "${{ item.key }}",
			Recipe:   "${{ item.recipe }}",
			Required: "${{ index == 0 }}",
			When:     "item.enabled",
			Inputs: map[string]interface{}{
				"value": "${{ item.value }}",
			},
		},
	})
	if err != nil {
		t.Fatalf("render child group: %v", err)
	}
	if len(out.Children) != 2 {
		t.Fatalf("expected two children, got %#v", out.Children)
	}
	if out.Children[0].Key != "first" || out.Children[0].Recipe != "child-a" || !out.Children[0].Required || out.Children[0].Inputs["value"] != "alpha" || out.Children[0].Skipped {
		t.Fatalf("unexpected first child: %#v", out.Children[0])
	}
	if out.Children[1].Key != "second" || out.Children[1].Recipe != "child-b" || out.Children[1].Required || !out.Children[1].Skipped {
		t.Fatalf("unexpected second child: %#v", out.Children[1])
	}
}

func TestRenderChildGroupInputRejectsDuplicateKeys(t *testing.T) {
	resCtx := newChildGroupRenderContext(t, nil, nil)
	_, err := renderChildGroupInput(resCtx, recipe.ChildGroupData{
		Children: []recipe.ChildGroupChild{
			{Key: "same", Recipe: "child-a"},
			{Key: "same", Recipe: "child-b"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `child_group child key "same" is duplicated`) {
		t.Fatalf("expected duplicate key error, got %v", err)
	}
}

func TestRenderChildGroupInputResolvesArtifactNameShorthand(t *testing.T) {
	ref := recipeartifacts.NewExternalRef("ticket", "https://example.test/ticket.json", false)
	resCtx := newChildGroupRenderContext(t, nil, map[string]recipeartifacts.Ref{"ticket": ref})

	out, err := renderChildGroupInput(resCtx, recipe.ChildGroupData{
		Artifacts: recipe.ChildGroupArtifacts{Use: []interface{}{"ticket"}},
		Children: []recipe.ChildGroupChild{
			{Key: "child", Recipe: "child-a"},
		},
	})
	if err != nil {
		t.Fatalf("render child group: %v", err)
	}
	if len(out.Children) != 1 || len(out.Children[0].Artifacts) != 1 || out.Children[0].Artifacts[0].NameValue() != "ticket" {
		t.Fatalf("expected ticket artifact, got %#v", out.Children)
	}
}

func TestRenderChildGroupInputErrorsOnUnknownArtifactName(t *testing.T) {
	resCtx := newChildGroupRenderContext(t, nil, nil)
	_, err := renderChildGroupInput(resCtx, recipe.ChildGroupData{
		Artifacts: recipe.ChildGroupArtifacts{Use: []interface{}{"missing"}},
		Children: []recipe.ChildGroupChild{
			{Key: "child", Recipe: "child-a"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `artifact "missing" not found`) {
		t.Fatalf("expected missing artifact error, got %v", err)
	}
}

func TestChildGroupInternalOpModes(t *testing.T) {
	start, err := childGroupInternalOp("start")
	if err != nil || start == "" {
		t.Fatalf("expected start op, got %q err=%v", start, err)
	}
	run, err := childGroupInternalOp("")
	if err != nil || run == "" {
		t.Fatalf("expected default run op, got %q err=%v", run, err)
	}
	_, err = childGroupInternalOp("race")
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
}
