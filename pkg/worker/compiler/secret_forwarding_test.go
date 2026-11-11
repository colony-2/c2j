package compiler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

type secretIncludeJobContext struct {
	*countingJobContext
	secretCalls int
}

func (c *secretIncludeJobContext) ResolveSecrets(context.Context, []string) (jobdb.SecretResolveResult, error) {
	c.secretCalls++
	return jobdb.SecretResolveResult{}, fmt.Errorf("unexpected plaintext resolution")
}
func (c *secretIncludeJobContext) ResolveSecretsFresh(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
	return c.ResolveSecrets(ctx, names)
}

func TestSecretIncludeBoundarySchemaAndOutputs(t *testing.T) {
	dir := t.TempDir()
	rootFile := filepath.Join(dir, "root.yaml")
	included := filepath.Join(dir, "included.yaml")

	for _, kind := range []string{"sequence", "state"} {
		for _, forwarded := range []bool{false, true} {
			for _, caught := range []bool{false, true} {
				for _, marked := range []bool{true, false} {
					t.Run(fmt.Sprintf("%s/forwarded=%t/catch=%t/secret=%t", kind, forwarded, caught, marked), func(t *testing.T) {
						body := "sequence: []\n"
						if kind == "state" {
							body = "state:\n  initial: done\n  states:\n    done:\n      sequence: []\n"
						}
						catch := ""
						if caught {
							catch = "catch:\n  - when: 'true'\n    continue: {outputs: {ok: true}}\n"
						}
						require.NoError(t, os.WriteFile(included, []byte(fmt.Sprintf("id: included\ninput_schema:\n  token: {type: string, secret: %t, required: true}\n%s%s", marked, catch, body)), 0600))
						binding := "${{ context.secrets.token }}"
						if forwarded {
							require.NoError(t, os.WriteFile(filepath.Join(dir, "containing.yaml"), []byte("id: containing\ninput_schema:\n  token: {type: string, secret: true, required: true}\ninputs: {token: '${{ inputs.token }}'}\nsequence:\n  - include: ./included.yaml\n    perms: {limit: {jobdb: {}}}\n    inputs: {token: '${{ inputs.token }}'}\n"), 0600))
						}
						target := "included.yaml"
						if forwarded {
							target = "containing.yaml"
						}
						r, err := recipe.LoadRecipeFromString([]byte(fmt.Sprintf("id: root\nsequence:\n  - include: ./%s\n    perms: {limit: {jobdb: {}}}\n    inputs: {token: '%s'}\n", target, binding)))
						require.NoError(t, err)
						expanded, err := ResolveInlineRecipes(celltest.Context(context.Background()), *r, InlineResolutionOptions{RootFile: rootFile})
						require.NoError(t, err)
						job, git := GenerateTestContext()
						// Any plaintext lookup fails; composite entry must retain opaque references.
						jobContext := &secretIncludeJobContext{countingJobContext: &countingJobContext{jobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}}}
						c := newWorkflowContext(jobContext)
						for _, mode := range []ExecutionMode{ExecutionModeValidate, ExecutionModeRun} {
							for _, validation := range []ValidationMode{ValidateAll, ValidatePathOnly} {
								_, _, err = ExecuteRecipe(c, expanded.Recipe, nil, job, git, ExecutionOptions{Mode: mode, Validation: ValidationOptions{Mode: validation}})
								if marked {
									require.NoError(t, err, "mode=%s validation=%s", mode, validation)
								} else {
									require.ErrorContains(t, err, "not marked secret")
								}
							}
						}
						require.Zero(t, jobContext.secretCalls)
					})
				}
			}
		}
	}

	ctx := newChildGroupRenderContext(t, nil, nil)
	input, err := renderChildInputs(ctx, map[string]any{"token": "${{ context.secrets.token }}"}, nil)
	require.NoError(t, err)
	require.Equal(t, secrets.Reference{Name: "token"}, input["token"])
	_, err = ctx.ResolveMap(map[string]any{"token": "${{ {'$c2j_secret': 'token'} }}"})
	require.Error(t, err)
}

func TestSecretIncludeValidationSchemaContract(t *testing.T) {
	for _, kind := range []string{"sequence", "state"} {
		for _, tc := range []struct {
			name, schema, binding, message string
		}{
			{"required", "input_schema:\n  token: {type: string, required: true}\n", "{}", "required"},
			{"type", "input_schema:\n  token: {type: number}\n", "{token: wrong}", "number"},
			{"default", "input_schema:\n  token: {type: string, default_value: default}\n", "{}", ""},
			{"legacy", "", "{token: value}", ""},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				body := "sequence: []\n"
				if kind == "state" {
					body = "state:\n  initial: done\n  states:\n    done:\n      sequence: []\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(dir, "included.yaml"), []byte("id: included\n"+tc.schema+body), 0600))
				r, err := recipe.LoadRecipeFromString([]byte("id: root\nsequence:\n  - include: ./included.yaml\n    perms: {limit: {jobdb: {}}}\n    inputs: " + tc.binding + "\n"))
				require.NoError(t, err)
				expanded, err := ResolveInlineRecipes(celltest.Context(context.Background()), *r, InlineResolutionOptions{RootFile: filepath.Join(dir, "root.yaml")})
				require.NoError(t, err)
				err = validateCatchRecipe(t, expanded.Recipe)
				if tc.message == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tc.message)
				}
			})
		}
	}
}
