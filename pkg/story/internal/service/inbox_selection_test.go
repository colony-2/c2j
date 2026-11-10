package service

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/story/internal/inbox"
	"github.com/colony-2/c2j/pkg/worker/compiler"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

// Compare inspection with the compiler's actual selection boundary, rather than
// assuming that descriptor-shaped JSON constitutes an artifact selection.
func TestInboxSelectionsFollowCompilerBoundaries(t *testing.T) {
	type input struct {
		Document  map[string]any `json:"document"`
		Documents []any          `json:"documents"`
		Selected  *artifacts.Ref `json:"selected"`
		Padding   string         `json:"padding"`
	}
	ordinary := func(name, value string) map[string]any {
		return map[string]any{"kind": "value", "name": name, "format": "text", "value": value}
	}
	selected := artifacts.Ref{Kind: artifacts.RefKindValue, Name: "selected.json", Format: "json", Value: json.RawMessage(`{"large":9007199254740993123456789,"null":null}`)}
	for _, tc := range []struct {
		name     string
		raw      map[string]any
		bindings int
	}{
		{"ordinary map", map[string]any{"document": ordinary("phantom.txt", "ordinary data")}, 0},
		{"ordinary arrays with conflicting names", map[string]any{"documents": []any{ordinary("phantom.txt", "first"), ordinary("phantom.txt", "second"), map[string]any{"nested": ordinary("phantom.txt", "third")}}}, 0},
		{"typed selection and conflicting ordinary names", map[string]any{"selected": selected, "document": ordinary("selected.json", "unselected conflicting data"), "documents": []any{ordinary("phantom.txt", "not a binding"), ordinary("selected.json", "another unselected conflict")}}, 1},
	} {
		for _, private := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/inline", true: "/private"}[private], func(t *testing.T) {
				raw := make(map[string]any, len(tc.raw)+1)
				for k, v := range tc.raw {
					raw[k] = v
				}
				if private {
					raw["padding"] = strings.Repeat("x", 140000)
				}
				normalized, err := compiler.NormalizeOpInput(reflect.TypeOf(input{}), raw)
				require.NoError(t, err)
				require.Len(t, normalized.ValueArtifacts, tc.bindings)
				// The compiler merges only collected typed values into invocation.Artifacts.
				td, err := workerops.NewInvocationTaskData(workerops.ActivityInvocationRequest{Input: normalized.Data, Artifacts: normalized.ValueArtifacts})
				require.NoError(t, err)
				data, err := td.GetData()
				require.NoError(t, err)
				projection := inbox.Project(context.Background(), td)
				require.Equal(t, "available", projection.Status)
				require.Len(t, projection.Entries, tc.bindings)
				svc, engine, req := inboxService(t, string(data))
				if private {
					arts, err := td.GetArtifacts()
					require.NoError(t, err)
					require.Len(t, arts, 1)
					engine.artifact = arts[0]
					engine.run.Attempts[0].Tasks[0].Attempts[0].Input.Artifacts = []jobdb.ArtifactInfo{{Name: arts[0].Name(), Key: &jobdb.ArtifactKey{JobId: "job", TaskOrdinal: 7, Name: arts[0].Name()}}}
				}
				req.Binding = "phantom.txt"
				_, err = svc.GetInboxValue(context.Background(), req)
				require.EqualError(t, err, "inbox: binding_unknown")
				req.Binding = "selected.json"
				got, err := svc.GetInboxValue(context.Background(), req)
				if tc.bindings == 0 {
					require.EqualError(t, err, "inbox: binding_unknown")
				} else {
					require.NoError(t, err)
					require.Equal(t, "selected.json", projection.Entries[0].Binding)
					require.Equal(t, "{\"large\":9007199254740993123456789,\"null\":null}\n", string(got.Data))
				}
			})
		}
	}
}

func TestInboxCompilerRejectsConflictingTypedSelections(t *testing.T) {
	type input struct {
		Documents []artifacts.Ref `json:"documents"`
	}
	first := artifacts.Ref{Kind: artifacts.RefKindValue, Name: "same.txt", Format: "text", Value: json.RawMessage(`"first"`)}
	second := first
	second.Value = json.RawMessage(`"second"`)
	_, err := compiler.NormalizeOpInput(reflect.TypeOf(input{}), map[string]any{"documents": []any{first, second}})
	require.ErrorContains(t, err, "duplicate value artifact name")
	// Identical typed references select one binding, just as compiler accumulation does.
	normalized, err := compiler.NormalizeOpInput(reflect.TypeOf(input{}), map[string]any{"documents": []any{first, first}})
	require.NoError(t, err)
	require.Len(t, normalized.ValueArtifacts, 1)
	td, err := workerops.NewInvocationTaskData(workerops.ActivityInvocationRequest{Input: normalized.Data, Artifacts: normalized.ValueArtifacts})
	require.NoError(t, err)
	got := inbox.Project(context.Background(), td)
	require.Len(t, got.Entries, 1)
	require.Equal(t, "same.txt", got.Entries[0].Binding)
}
