package recipe

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestExecutionFailurePresentation(t *testing.T) {
	for _, p := range []FailurePresentation{
		{OutputExitFormat, "Required tool unavailable: go [exit 1]"},
		{"future", "message"}, {OutputExitFormat, ""}, {OutputExitFormat, " \t"},
		{OutputExitFormat, "line\nbreak"}, {OutputExitFormat, "x\x1b[31m"}, {OutputExitFormat, "bad\xff"},
		{OutputExitFormat, strings.Repeat("x", 2049)}, {OutputExitFormat, "x\u2028y"},
	} {
		f := &RuntimeFailure{Message: p.Message, Attrs: map[string]any{"command": map[string]any{"presentation": p}}}
		_, ok := CompactFailureMessage(f)
		if p.Message == "bad\xff" {
			require.Nil(t, ExecutionPresentation(f.Attrs))
		}
		require.Equal(t, p.Message == "Required tool unavailable: go [exit 1]", ok)
		appWithValue := &jobdb.AppError{Payload: jobdb.AppErrorPayload{Attrs: map[string]any{"c2j_failure": f}}}
		_, errorOK := CompactErrorMessage(appWithValue)
		require.Equal(t, ok, errorOK)
		if ok {
			data, err := json.Marshal(f)
			require.NoError(t, err)
			var restored RuntimeFailure
			require.NoError(t, json.Unmarshal(data, &restored))
			require.Equal(t, p.Message, ExecutionPresentation(restored.Clone().Attrs).Message)
			app := &jobdb.AppError{Payload: jobdb.AppErrorPayload{Message: "wrapper", Attrs: map[string]any{"c2j_failure": restored}}}
			got, ok := CompactErrorMessage(fmt.Errorf("CLI: %w", app))
			require.True(t, ok)
			require.Equal(t, p.Message, got)
			f.Message = "replacement"
			_, ok = CompactFailureMessage(f)
			require.False(t, ok)
			outer := &RuntimeFailure{Message: p.Message, Cause: &restored}
			_, ok = CompactFailureMessage(outer)
			require.False(t, ok)
		}
	}
	for _, attrs := range []map[string]any{nil, {"command": true}, {"extension": map[string]any{"presentation": 42}}, {"command": map[string]any{"presentation": map[string]any{"format": OutputExitFormat, "message": 7}}}} {
		require.Nil(t, ExecutionPresentation(attrs))
	}
	_, ok := CompactFailureMessage(&RuntimeFailure{Message: "looks compact [exit 1]"})
	require.False(t, ok)
}
