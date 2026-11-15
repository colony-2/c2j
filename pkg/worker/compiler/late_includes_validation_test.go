package compiler

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/stretchr/testify/require"
)

func TestLateIncludeAuthoredValidationWithoutSource(t *testing.T) {
	for _, state := range []bool{false, true} {
		for _, invalid := range []string{"", "selector", "inputs", "when", "vars", "contract", "external_schema", "permissions", "missing_id"} {
			t.Run(fmt.Sprintf("state=%t/%s", state, invalid), func(t *testing.T) {
				selector := "git+https://unavailable.example/repo//absent.yaml@1111111111111111111111111111111111111111"
				extra := ""
				id := "id: call\n"
				switch invalid {
				case "selector":
					selector = "${{ unknown_function() }}"
				case "inputs":
					extra = "inputs: {x: '${{ unknown_function() }}'}\n"
				case "when":
					extra = "when: unknown_function()\n"
				case "vars":
					extra = "vars: {x: '${{ unknown_function() }}'}\n"
				case "contract":
					extra = "expect_outputs: {type: invalid}\n"
				case "external_schema":
					extra = "expect_outputs: {$ref: 'https://unavailable.example/schema'}\n"
				case "permissions":
					extra = "perms: {unknown: true}\n"
				case "missing_id":
					id = ""
				}
				call := id + "include: {bind: late, recipe: " + fmt.Sprintf("%q", selector) + "}\n" + extra
				body := "id: root\nsequence:\n- " + indentLate(call, "  ")[2:]
				if state {
					body = "id: root\nstate:\n initial: call\n states:\n  call:\n" + indentLate(call, "   ")
				}
				rec, err := recipe.LoadRecipeFromString([]byte(body))
				if invalid == "permissions" {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				expanded, err := ResolveInlineRecipes(context.Background(), *rec, InlineResolutionOptions{})
				if invalid == "missing_id" && !state {
					require.ErrorContains(t, err, "explicit id")
					return
				}
				if invalid == "contract" || invalid == "external_schema" {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				err = validateCatchRecipe(t, expanded.Recipe)
				if invalid == "" || invalid == "missing_id" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}
func indentLate(s, prefix string) string {
	out := ""
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		out += prefix + line + "\n"
	}
	return out
}

func TestLateIncludeOutputValidationPlaceholders(t *testing.T) {
	for _, contract := range []bool{false, true} {
		extra := ""
		if contract {
			extra = "  expect_outputs: {type: object, properties: {status: {type: string}}, required: [status]}\n"
		}
		body := "id: root\nsequence:\n- id: call\n  include: {bind: late, recipe: 'git+https://unavailable.example/repo//absent.yaml@1111111111111111111111111111111111111111'}\n" + extra
		if contract {
			body += "outputs: {value: '${{ sequence.call.outputs.status + \"-ok\" }}'}\n"
		}
		rec, err := recipe.LoadRecipeFromString([]byte(body))
		require.NoError(t, err)
		expanded, err := ResolveInlineRecipes(context.Background(), *rec, InlineResolutionOptions{})
		require.NoError(t, err)
		require.NoError(t, validateCatchRecipe(t, expanded.Recipe))
		include := expanded.Recipe.RecipeImpl.(*recipe.RecipeSequence).Sequence[0].NodeImpl.(*recipe.NodeInclude)
		placeholders := hookOutputPlaceholder(include)
		if contract {
			require.Contains(t, placeholders, "status")
		} else {
			require.Empty(t, placeholders)
		}
	}
}
