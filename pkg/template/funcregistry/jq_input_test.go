package funcregistry

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/ext"
	"github.com/stretchr/testify/require"
)

func TestJQTypedArtifactInputs(t *testing.T) {
	const large = int64(9007199254740993)
	key := jobdb.ArtifactKey{JobId: "producer", TaskOrdinal: 17, Name: "review.md", SizeBytes: large}
	stored := artifacts.NewStoredRef(key)
	value := artifacts.Ref{Kind: artifacts.RefKindValue, Name: "data.json", Format: "json", Value: json.RawMessage(`{"n":9007199254740993,"text":" λ\n${{ untouched }}"}`)}
	refs := map[string]artifacts.Ref{"review.md": stored, "data.json": value}
	numbers := map[string]any{"numbers": []any{large}}
	builder := NewBuilder().WithDefaults()
	jq := builder.TemplateFuncsWithContext(nil)["jq"].(func(any, string) (any, error))
	env, err := cel.NewEnv(cel.Variable("input", cel.DynType), cel.Variable("query", cel.StringType), ext.NativeTypes(
		reflect.TypeOf(artifacts.Ref{}), reflect.TypeOf(artifacts.StoredRef{}), reflect.TypeOf(artifacts.ExternalRef{}), reflect.TypeOf(jobdb.ArtifactKey{}), ext.ParseStructTag("json"),
	))
	require.NoError(t, err)
	opts, err := builder.FunctionOptions(env.CELTypeAdapter())
	require.NoError(t, err)
	env, err = env.Extend(opts...)
	require.NoError(t, err)

	cases := []struct {
		name  string
		input any
		query string
		want  any
	}{
		{"nil artifact map", map[string]artifacts.Ref(nil), `. + {"feedback.json": true}`, map[string]any{"feedback.json": true}},
		{"empty artifact map", map[string]artifacts.Ref{}, `. + {"feedback.json": true}`, map[string]any{"feedback.json": true}},
		{"merge nested maps", []any{map[string]artifacts.Ref(nil), refs}, `add | keys`, []string{"data.json", "review.md"}},
		{"reference JSON shape", refs, `.`, refs},
		{"reference fields", refs, `.["review.md"].stored.key | [.jobId, .taskOrdinal, .name, .sizeBytes + 1]`, []any{"producer", 17, "review.md", large + 1}},
		{"value JSON", refs, `.["data.json"].value | [.n + 1, .text]`, []any{large + 1, " λ\n${{ untouched }}"}},
		{"value number", refs, `.["data.json"].value.n`, large},
		{"typed slice", []artifacts.Ref{stored}, `.[0].name`, "review.md"},
		{"pointer", map[string]*artifacts.Ref{"ref": &stored, "absent": nil}, `[.ref.kind, .absent]`, []any{"stored", nil}},
		{"stored key", map[string]jobdb.ArtifactKey{"ref": key}, `.ref.sizeBytes + 1`, large + 1},
		{"external reference", artifacts.NewExternalRef("doc", "https://example.com/doc", true), `[.name, .external.url, .external.expand]`, []any{"doc", "https://example.com/doc", true}},
		{"omitted fields", artifacts.Ref{Kind: artifacts.RefKindAttached, Name: "doc"}, `keys`, []string{"kind", "name"}},
		{"detached generic containers", numbers, `.numbers[0] += 1`, map[string]any{"numbers": []any{large + 1}}},
		{"large unsigned integer", map[string]any{"n": ^uint64(0)}, `.n | tostring`, "18446744073709551615"},
		{"large JSON integer", map[string]any{"value": json.RawMessage(`{"n":18446744073709551616}`)}, `.value.n | tostring`, "18446744073709551616"},
		{"opaque bookkeeping", map[string]any{"jobs": jobcontext.EmptyStartedJobsContext()}, `. + {"ok": true}`, map[string]any{"jobs": jobcontext.EmptyStartedJobsContext(), "ok": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.input)
			require.NoError(t, err)
			want, err := json.Marshal(tc.want)
			require.NoError(t, err)
			// Compare object contents without rounding large integers.
			decodeJSON := func(raw []byte) any {
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				var value any
				require.NoError(t, decoder.Decode(&value))
				return value
			}
			out, err := jq(tc.input, tc.query)
			require.NoError(t, err)
			encoded, err := json.Marshal(out)
			require.NoError(t, err)
			require.Equal(t, decodeJSON(want), decodeJSON(encoded))
			// Exercise CEL's actual container wrappers, including the literal list
			// used to merge review documents, as well as the member overload.
			for _, expr := range []string{`jq(input, query)`, `input.jq(query)`, `jq([input], ".[0] | " + query)`} {
				ast, issues := env.Compile("json_stringify(" + expr + ")")
				require.NoError(t, issues.Err())
				program, err := env.Program(ast)
				require.NoError(t, err)
				result, _, err := program.Eval(map[string]any{"input": tc.input, "query": tc.query})
				require.NoError(t, err)
				require.Equal(t, decodeJSON(want), decodeJSON([]byte(result.Value().(string))))
			}
			after, err := json.Marshal(tc.input)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "jq must not mutate recipe context")
		})
	}
	require.Equal(t, large, refs["review.md"].Stored.Key.SizeBytes)
	require.Equal(t, large, numbers["numbers"].([]any)[0], "normalization must not replace stored integer types")
}

func TestJQRejectsUnsupportedInputWithoutPanic(t *testing.T) {
	jq := NewBuilder().WithDefaults().TemplateFuncsWithContext(nil)["jq"].(func(any, string) (any, error))
	for _, input := range []any{map[any]any{1: "number", "1": "string"}, json.RawMessage(`{"broken":`), make(chan int)} {
		_, err := jq(input, ".")
		require.ErrorContains(t, err, "jq: invalid input:")
		require.NotContains(t, err.Error(), "PANIC")
	}
}
