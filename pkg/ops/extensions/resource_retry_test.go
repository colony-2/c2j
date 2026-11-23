package extensions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/resourceretry"
	"github.com/colony-2/c2j/pkg/ops/process"
	"github.com/stretchr/testify/require"
)

const validResourceControl = `{"control":{"type":"resource_retry","version":1,"reason":"resource_temporarily_unavailable","source":"local_spawn","phase":"launch","replay":"restart_from_input"}}`

func TestResourceRetryControlDecoder(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"launch", validResourceControl, true},
		{"preparation", strings.ReplaceAll(validResourceControl, `"launch"`, `"preparation"`), true},
		{"diagnostic", strings.ReplaceAll(strings.ReplaceAll(validResourceControl, `"launch"`, `"preparation"`), `"local_spawn"`, `"preparation_diagnostic"`), true},
		{"unknown root", strings.Replace(validResourceControl, `{"control":`, `{"other":1,"control":`, 1), false},
		{"mixed output", strings.Replace(validResourceControl, `{"control":`, `{"output":{},"control":`, 1), false},
		{"mixed objects", strings.Replace(validResourceControl, `{"control":`, `{"objects":{},"control":`, 1), false},
		{"mixed artifacts", strings.Replace(validResourceControl, `{"control":`, `{"artifact_refs":{},"control":`, 1), false},
		{"duplicate root", `{"control":{},` + validResourceControl[1:], false},
		{"duplicate body", strings.Replace(validResourceControl, `"type":`, `"version":1,"type":`, 1), false},
		{"unknown body", strings.Replace(validResourceControl, `"type":`, `"extra":true,"type":`, 1), false},
		{"missing", strings.Replace(validResourceControl, `"version":1,`, ``, 1), false},
		{"null", strings.Replace(validResourceControl, `"version":1`, `"version":null`, 1), false},
		{"string version", strings.Replace(validResourceControl, `"version":1`, `"version":"1"`, 1), false},
		{"float version", strings.Replace(validResourceControl, `"version":1`, `"version":1.0`, 1), false},
		{"future", strings.Replace(validResourceControl, `"version":1`, `"version":2`, 1), false},
		{"case", strings.Replace(validResourceControl, `"version"`, `"Version"`, 1), false},
		{"source", strings.ReplaceAll(validResourceControl, `local_spawn`, `arbitrary_output`), false},
		{"phase", strings.ReplaceAll(validResourceControl, `launch`, `execution`), false},
		{"unsafe replay", strings.ReplaceAll(validResourceControl, `restart_from_input`, `resume_session`), false},
		{"wrong reason", strings.ReplaceAll(validResourceControl, `resource_temporarily_unavailable`, `network`), false},
		{"trailing", validResourceControl + `{}`, false},
		{"incomplete", validResourceControl[:len(validResourceControl)-1], false},
		{"oversized", strings.Repeat(" ", 4096) + validResourceControl, false},
		{"array", `[]`, false},
		{"root null", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeResourceRetry([]byte(tc.raw))
			require.Equal(t, tc.valid, err == nil, "%v", err)
		})
	}
}

func TestResourceRetryExtensionExitSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, raw, extra          string
		code                      int
		support, restart, success bool
	}{
		{name: "negotiated", raw: validResourceControl, code: 1, support: true, restart: true},
		{name: "old reader", raw: validResourceControl, code: 1},
		{name: "exit zero", raw: validResourceControl, support: true},
		{name: "exit zero without negotiation", raw: validResourceControl},
		{name: "extension wait exit", raw: validResourceControl, code: 76, support: true},
		{name: "wait on resource exit", raw: `{"wait":{"version":1,"until":"2020-01-01T00:00:00Z"}}`, code: 1, support: true},
		{name: "mixed wait", raw: strings.Replace(validResourceControl, `{"control":`, `{"wait":{"version":1},"control":`, 1), code: 1, support: true},
		{name: "exit 75", raw: validResourceControl, code: 75, support: true},
		{name: "other failure", raw: validResourceControl, code: 7, support: true},
		{name: "mixed", raw: strings.Replace(validResourceControl, `{"control":`, `{"output":{"ready":true},"control":`, 1), code: 1, support: true},
		{name: "launched body text", raw: `{"error":"fork/exec git: resource temporarily unavailable"}`, code: 1, support: true},
		{name: "ordinary producer", raw: `{"value":"ok"}`, support: true, success: true},
		{name: "incomplete capture", raw: validResourceControl, code: 1, support: true, extra: `head -c 5000000 /dev/zero >&2;`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := resourceretry.WithSupport(celltest.Context(context.Background()), tc.support)
			run := fmt.Sprintf(`printf '%%s' '%s'; %s exit %d`, tc.raw, tc.extra, tc.code)
			deps := diagnosticExtension(t, run, "{type: object}")
			out, err := executeExtension(deps, ctx, ExecutionInput{Selector: "./check", Env: map[string]string{resourceretry.CapabilityEnv: "forgery"}})
			require.Equal(t, tc.restart, resourceretry.Is(err), "%v", err)
			if tc.restart {
				require.Nil(t, out)
				require.Empty(t, deps.GetOutputArtifacts(), "a wait must not publish a failed execution or successful outputs")
			} else if tc.success {
				require.NoError(t, err)
				require.Equal(t, "ok", out["value"])
			} else {
				require.Error(t, err)
				require.Nil(t, out)
			}
		})
	}
	ctx, cancel := context.WithTimeout(resourceretry.WithSupport(celltest.Context(context.Background()), true), 80*time.Millisecond)
	defer cancel()
	deps := diagnosticExtension(t, fmt.Sprintf(`printf '%%s' '%s'; sleep 2; exit 1`, validResourceControl), "{type: object}")
	_, err := executeExtension(deps, ctx, ExecutionInput{Selector: "./check"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, resourceretry.Is(err))
}

func TestResourceRetryRejectSuccessEnvelopes(t *testing.T) {
	for _, raw := range []string{validResourceControl, `{"output":{},"control":{"type":"resource_retry"}}`, `{"control":{"type":"resource_retry","version":2}}`} {
		_, _, err := decodeExecutionEnvelope([]byte(raw))
		require.Error(t, err)
	}
	for _, raw := range []string{`{"control":null}`, `{"control":{"type":"business-value"}}`, `{"output":{"control":{"type":"resource_retry"}}}`} {
		outputs, _, err := decodeExecutionEnvelope([]byte(raw))
		require.NoError(t, err)
		require.Contains(t, outputs, "control")
	}
}

func TestResourceRetryExtensionCleanupRequired(t *testing.T) {
	exitErr := errors.New("extension exited with status 1")
	for _, capture := range []*process.Capture{
		{}, // No launched managed tree stop was observed.
		{TreeCleanupError: errors.New("kill process group: operation not permitted")},
	} {
		err := resourceRetryAfterExit([]byte(validResourceControl), capture, exitErr)
		require.ErrorIs(t, err, exitErr)
		require.ErrorContains(t, err, "process-tree cleanup was not confirmed")
		require.False(t, resourceretry.Is(err))
	}
}

func TestResourceRetryExtensionStopsManagedChildren(t *testing.T) {
	for _, redirect := range []string{"", " >/dev/null 2>&1"} {
		t.Run(fmt.Sprintf("redirected=%t", redirect != ""), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "survived")
			ctx, cancel := context.WithTimeout(resourceretry.WithSupport(celltest.Context(context.Background()), true), 3*time.Second)
			defer cancel()
			run := fmt.Sprintf(`(sleep 0.3; printf survived > %q)%s & printf '%%s' '%s'; exit 1`, marker, redirect, validResourceControl)
			deps := diagnosticExtension(t, run, "{type: object}")
			out, err := executeExtension(deps, ctx, ExecutionInput{Selector: "./check"})
			require.True(t, resourceretry.Is(err), "%v", err)
			require.Nil(t, out)
			require.Empty(t, deps.GetOutputArtifacts())
			time.Sleep(450 * time.Millisecond)
			require.NoFileExists(t, marker, "managed child must be stopped before a resource retry")
		})
	}
}

func TestResourceRetryExtensionBusinessControl(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("negotiated=%t", supported), func(t *testing.T) {
			ctx := resourceretry.WithSupport(celltest.Context(context.Background()), supported)
			deps := diagnosticExtension(t, `printf '%s' '{"control":"business-value"}'`, "{type: object, additionalProperties: false, required: [control], properties: {control: {type: string}}}")
			out, err := executeExtension(deps, ctx, ExecutionInput{Selector: "./check"})
			require.NoError(t, err)
			require.Equal(t, map[string]any{"control": "business-value"}, out)
		})
	}
}
