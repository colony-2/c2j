package compiler

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/git/thinpackrebase"
	coreops "github.com/colony-2/c2j/pkg/ops"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestResolvedTreeSchemaAndExplicitConfirmations(t *testing.T) {
	for _, op := range []coreops.RegisterableOp{thinpackrebase.GetOp(), thinpackrebase.GetRebaseOp()} {
		t.Run(op.GetMetadata().Type, func(t *testing.T) {
			typ := op.TaskChain()[0].InputType
			schema, err := workerops.NewDefaultSchemaGenerator().GenerateSchema(typ)
			require.NoError(t, err)
			raw, err := json.Marshal(schema)
			require.NoError(t, err)
			var doc any
			require.NoError(t, json.Unmarshal(raw, &doc))
			compiler := jsonschema.NewCompiler()
			require.NoError(t, compiler.AddResource("https://test.invalid/rebase", doc))
			compiled, err := compiler.Compile("https://test.invalid/rebase")
			require.NoError(t, err)
			for _, mode := range []string{"complete", "replay", "null", "empty", "scalar", "unknown", "missing-base", "missing-persist", "missing-target", "missing-tree", "short", "task-default"} {
				t.Run(mode, func(t *testing.T) {
					input := map[string]any{"repo_path": "/unused", "base_hash": strings.Repeat("a", 40), "persist_hash": strings.Repeat("b", 40), "target_base_hash": strings.Repeat("c", 40), "resolution": map[string]any{"tree_hash": strings.Repeat("d", 40)}}
					switch mode {
					case "replay":
						delete(input, "resolution")
						delete(input, "persist_hash")
						input["base_hash"] = "abcdef0"
					case "null":
						input["resolution"] = nil
					case "empty", "missing-tree":
						input["resolution"] = map[string]any{}
					case "scalar":
						input["resolution"] = "resolved"
					case "unknown":
						input["resolution"].(map[string]any)["unknown"] = "value"
					case "missing-base", "missing-persist", "missing-target":
						name := map[string]string{"missing-base": "base_hash", "missing-persist": "persist_hash", "missing-target": "target_base_hash"}[mode]
						delete(input, name)
					case "short":
						input["persist_hash"] = "abcdef0"
					case "task-default":
						input["base_hash"] = contextual.GitExecutionBaseSentinel
					}
					valid := mode == "complete" || mode == "replay"
					if valid {
						require.NoError(t, compiled.Validate(input))
						require.NoError(t, validateOpInputType(typ, input, false))
						require.NoError(t, coreops.ValidateMaterializedOpInputType(typ, input))
					} else {
						require.Error(t, compiled.Validate(input))
						require.Error(t, validateOpInputType(typ, input, false))
						require.Error(t, coreops.ValidateMaterializedOpInputType(typ, input))
					}
					encoded, err := json.Marshal(input)
					require.NoError(t, err)
					var typed thinpackrebase.ThinpackRebaseInput
					if valid {
						require.NoError(t, json.Unmarshal(encoded, &typed))
					} else {
						require.Error(t, json.Unmarshal(encoded, &typed))
					}
					// A missing confirmation must fail before defaults can fill it.
					if strings.HasPrefix(mode, "missing-") || mode == "empty" || mode == "null" || mode == "scalar" {
						require.Error(t, workerops.InjectDefaults(typ, input))
					}
				})
			}
			replay := map[string]any{"target_base_hash": strings.Repeat("c", 40)}
			require.NoError(t, workerops.InjectDefaults(typ, replay))
			require.Equal(t, "{{ context.git.execution.base_hash }}", replay["base_hash"])
			require.Equal(t, "{{ context.git.execution.head_hash }}", replay["persist_hash"])
			require.NotContains(t, replay, "resolution")
			require.Equal(t, reflect.TypeFor[thinpackrebase.ThinpackRebaseInput](), typ)
		})
	}
}
