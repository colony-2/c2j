package funcregistry

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
)

func TestPathMatch(t *testing.T) {
	builder := NewBuilder().WithDefaults()
	env, err := cel.NewEnv(cel.Variable("reference", cel.StringType), cel.Variable("inventory", cel.ListType(cel.StringType)))
	require.NoError(t, err)
	opts, err := builder.FunctionOptions(env.CELTypeAdapter())
	require.NoError(t, err)
	env, err = env.Extend(opts...)
	require.NoError(t, err)
	ast, issues := env.Compile(`json_stringify(path_match(reference, inventory))`)
	require.NoError(t, issues.Err())
	program, err := env.Program(ast)
	require.NoError(t, err)

	type testCase struct {
		name, reference string
		inventory       []string
		status, path    string
		matches         []string
	}
	tests := []testCase{
		{"exact beats basename", "design.md", []string{"archive/design.md", "design.md"}, "matched", "design.md", []string{"design.md"}},
		{"outbox prefix", "outbox/design.md", []string{"design.md"}, "matched", "design.md", []string{"design.md"}},
		{"absolute", "/scratch/job/outbox/design.md", []string{"design.md"}, "matched", "design.md", []string{"design.md"}},
		{"normalized", "./outbox//design.md", []string{"design.md"}, "matched", "design.md", []string{"design.md"}},
		{"nested", "feature.md", []string{"iter1/consultant/feature.md"}, "matched", "iter1/consultant/feature.md", []string{"iter1/consultant/feature.md"}},
		{"longer suffix", "outbox/iter2/consultant/reply.md", []string{"iter1/consultant/reply.md", "iter2/consultant/reply.md"}, "matched", "iter2/consultant/reply.md", []string{"iter2/consultant/reply.md"}},
		{"ambiguous basename", "reply.md", []string{"iter2/reply.md", "iter1/reply.md"}, "ambiguous", "", []string{"iter1/reply.md", "iter2/reply.md"}},
		{"exact prefix", "outbox/design.md", []string{"outbox/design.md", "design.md"}, "matched", "outbox/design.md", []string{"outbox/design.md"}},
		{"missing", "missing.md", []string{"design.md"}, "not_found", "", nil},
		{"substring", "sign.md", []string{"design.md"}, "not_found", "", nil},
		{"case", "Design.md", []string{"design.md"}, "not_found", "", nil},
		{"extension", "design.txt", []string{"design.md"}, "not_found", "", nil},
		{"empty inventory", "design.md", []string{}, "not_found", "", nil},
		{"duplicates", "outbox/design.md", []string{"design.md", "design.md"}, "matched", "design.md", []string{"design.md"}},
		{"exact duplicates", "design.md", []string{"design.md", "design.md"}, "matched", "design.md", []string{"design.md"}},
		{"strongest ambiguous", "outbox/iter2/reply.md", []string{"reply.md", "b/iter2/reply.md", "a/iter2/reply.md", "a/iter2/reply.md"}, "ambiguous", "", []string{"a/iter2/reply.md", "b/iter2/reply.md"}},
		{"component boundary", "iter2/reply.md", []string{"notiter2/reply.md", "other/reply.md"}, "ambiguous", "", []string{"notiter2/reply.md", "other/reply.md"}},
		{"normalized is not exact", "./design.md", []string{"design.md", "archive/design.md"}, "ambiguous", "", []string{"archive/design.md", "design.md"}},
		{"internal dot", "//outbox/./docs//design.md", []string{"docs/design.md"}, "matched", "docs/design.md", []string{"docs/design.md"}},
		{"literal tilde", "~/design.md", []string{"~/design.md", "design.md"}, "matched", "~/design.md", []string{"~/design.md"}},
		{"literal environment variable", "$ROOT/design.md", []string{"$ROOT/design.md", "design.md"}, "matched", "$ROOT/design.md", []string{"$ROOT/design.md"}},
		{"literal URI components", "file:///outbox/design.md", []string{"outbox/design.md"}, "matched", "outbox/design.md", []string{"outbox/design.md"}},
		{"spaces preserved", "outbox/ design.md ", []string{" design.md "}, "matched", " design.md ", []string{" design.md "}},
		{"unicode", "outbox/设计.md", []string{"设计.md"}, "matched", "设计.md", []string{"设计.md"}},
	}
	for _, invalid := range []string{"", ".", "./", "/", "///", "design.md/", "design.md/.", "../design.md", "outbox/../design.md", "a/..", "a\x00.md", `outbox\design.md`} {
		tests = append(tests, testCase{"invalid reference " + invalid, invalid, []string{"design.md"}, "invalid", "", nil})
	}
	for _, invalid := range []string{"", "/design.md", "design.md/", "a//design.md", "./design.md", "a/./design.md", "../design.md", "a/../design.md", ".", "..", "a\x00.md", `a\design.md`} {
		tests = append(tests, testCase{"invalid inventory " + invalid, "design.md", []string{"design.md", invalid}, "invalid", "", nil})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches := tt.matches
			if matches == nil {
				matches = []string{}
			}
			want := map[string]any{"status": tt.status, "path": tt.path, "candidates": matches}
			original := slices.Clone(tt.inventory)
			require.Equal(t, want, pathMatch(tt.reference, tt.inventory))
			require.Equal(t, original, tt.inventory, "must not mutate inventory")
			reversed := slices.Clone(tt.inventory)
			slices.Reverse(reversed)
			require.Equal(t, want, pathMatch(tt.reference, reversed), "order must not affect results")
			out, _, err := program.Eval(map[string]any{"reference": tt.reference, "inventory": tt.inventory})
			require.NoError(t, err)
			encoded, err := json.Marshal(want)
			require.NoError(t, err)
			require.JSONEq(t, string(encoded), out.Value().(string))
		})
	}
}

func TestPathMatchArgumentTypes(t *testing.T) {
	builder := NewBuilder().WithDefaults()
	env, err := cel.NewEnv()
	require.NoError(t, err)
	opts, err := builder.FunctionOptions(env.CELTypeAdapter())
	require.NoError(t, err)
	env, err = env.Extend(opts...)
	require.NoError(t, err)
	for _, expr := range []string{
		`path_match(1, ["design.md"])`,
		`path_match("design.md", "design.md")`,
		`path_match("design.md", [1])`,
		`path_match(null, [])`,
		`path_match("design.md", null)`,
		`path_match("design.md", ["design.md", dyn(1)])`,
		`path_match("design.md", ["design.md", dyn(null)])`,
		`path_match("", [dyn(1)])`,
	} {
		t.Run(expr, func(t *testing.T) {
			ast, issues := env.Compile(expr)
			if issues.Err() != nil {
				return // Static type errors are also expression errors.
			}
			program, err := env.Program(ast)
			require.NoError(t, err)
			_, _, err = program.Eval(map[string]any{})
			require.Error(t, err)
		})
	}
}
