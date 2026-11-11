package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/jobrestart"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type restartProbe struct {
	requests []jobrestart.Request
	failure  error
}

func (p *restartProbe) Restart(_ context.Context, q jobrestart.Request) (*jobrestart.Response, error) {
	p.requests = append(p.requests, q)
	if p.failure != nil {
		return nil, p.failure
	}
	return &jobrestart.Response{
		Ref:     &jobref.Ref{JobID: "new", GoalID: "goal", TenantID: "tenant", Cell: "source"},
		Points:  &jobrestart.Points{ProjectID: "tenant", JobID: q.JobID, RestartScopes: []jobrestart.RestartScope{{ScopeID: "opaque", ChapterOrdinal: 9, Title: "Recorded title", Path: []string{"x"}, StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}},
		Context: &jobrestart.RestartContext{ProjectID: "tenant", JobID: q.JobID, ScopeID: "opaque", ChapterOrdinal: 9, Job: map[string]any{"git": map[string]any{"ref": "main"}}, Visible: map[string]any{"vars": map[string]any{"actual_name": "full"}, "inputs": nil, "sequence": map[string]any{"prior": map[string]any{"outputs": map[string]any{"secret": "[REDACTED]"}}}}, RedactedPaths: []string{"/visible/sequence/prior/outputs/secret"}},
	}, nil
}
func restartTestOpen(p *restartProbe) restartOpener {
	return func(context.Context, string) (jobrestart.API, func(), string, error) {
		return p, func() {}, "current", nil
	}
}
func executeRestartTest(c *cobra.Command, args ...string) (string, error) {
	var out bytes.Buffer
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(&out)
	c.SetErr(io.Discard)
	c.SetArgs(args)
	err := c.Execute()
	return out.String(), err
}
func TestRestartExplicitSelectors(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--scope", ""}, {"--scope", " "}, {"--chapter", "-1"}, {"--scope", "x", "--chapter", "0"}, {"--scope", "x", "--chapter", "2"},
	} {
		t.Run(joinArgs(args), func(t *testing.T) {
			p := &restartProbe{}
			_, err := executeRestartTest(newRestartCommand(restartTestOpen(p)), append([]string{"job", "--request-id", "id"}, args...)...)
			require.Error(t, err)
			require.Empty(t, p.requests)
			var uri string
			var embed, jsonOut bool
			_, err = executeRestartTest(newRestartInspection("context", &uri, &embed, &jsonOut, restartTestOpen(p)), append([]string{"job"}, args...)...)
			require.Error(t, err)
			require.Empty(t, p.requests)
		})
	}
	for _, args := range [][]string{{"--scope", "opaque"}, {"--chapter", "0"}, {"--chapter", "9"}} {
		p := &restartProbe{}
		out, err := executeRestartTest(newRestartCommand(restartTestOpen(p)), append([]string{"job", "--request-id", "id"}, args...)...)
		require.NoError(t, err)
		require.Contains(t, out, `"job_id":"new"`)
		require.Equal(t, "continue", p.requests[0].Goal)
	}
}
func joinArgs(args []string) string { raw, _ := json.Marshal(args); return string(raw) }
func TestRestartPatchFileAndLegacy(t *testing.T) {
	file := filepath.Join(t.TempDir(), "patch.json")
	require.NoError(t, os.WriteFile(file, []byte(`[{"op":"add","path":"/sequence/write/outputs/arbitrary","value":[1,null]}]`), 0600))
	p := &restartProbe{}
	out, err := executeRestartTest(newRestartCommand(restartTestOpen(p)), "self", "--scope", "opaque", "--request-id", "same", "--goal", "new", "--patch-file", file)
	require.NoError(t, err)
	require.Contains(t, out, `"cell":"source"`)
	q := p.requests[0]
	require.Equal(t, "current", q.JobID)
	require.Equal(t, "same", q.RequestID)
	require.Equal(t, "new", q.Goal)
	require.Equal(t, "/sequence/write/outputs/arbitrary", (*q.Patch)[0].Path)
	p.requests = nil
	_, err = executeRestartTest(newRestartCommand(restartTestOpen(p)), "job", "--scope", "opaque", "--request-id", "legacy")
	require.NoError(t, err)
	require.Nil(t, p.requests[0].Patch)
	for _, name := range []string{filepath.Join(t.TempDir(), "missing"), file} {
		require.NoError(t, os.WriteFile(file, []byte(`{"inputs":{}}`), 0600))
		p.requests = nil
		_, err = executeRestartTest(newRestartCommand(restartTestOpen(p)), "job", "--scope", "opaque", "--request-id", "bad", "--patch-file", name)
		require.Error(t, err)
		require.Empty(t, p.requests)
	}
}
func TestRestartInspectionOutput(t *testing.T) {
	var uri string
	var embed, jsonOut bool
	p := &restartProbe{}
	makeCmd := func(op string) *cobra.Command {
		return newRestartInspection(op, &uri, &embed, &jsonOut, restartTestOpen(p))
	}
	out, err := executeRestartTest(makeCmd("context"), "job", "--scope", "opaque")
	require.NoError(t, err)
	var snapshot jobrestart.RestartContext
	require.NoError(t, json.Unmarshal([]byte(out), &snapshot))
	require.Equal(t, "full", snapshot.Visible["vars"].(map[string]any)["actual_name"])
	require.Contains(t, snapshot.RedactedPaths, "/visible/sequence/prior/outputs/secret")
	file := filepath.Join(t.TempDir(), "context.json")
	stdout, err := executeRestartTest(makeCmd("context"), "job", "--chapter", "9", "--output", file)
	require.NoError(t, err)
	require.Empty(t, stdout)
	raw, err := os.ReadFile(file)
	require.NoError(t, err)
	require.JSONEq(t, out, string(raw))
	_, err = executeRestartTest(makeCmd("context"), "job", "--chapter", "9", "--output", file)
	require.Error(t, err)
	out, err = executeRestartTest(makeCmd("points"), "job")
	require.NoError(t, err)
	for _, value := range []string{"opaque", "9", "Recorded title", "2026-01-01", "[x]"} {
		require.Contains(t, out, value)
	}
	jsonOut = true
	out, err = executeRestartTest(makeCmd("points"), "job")
	require.NoError(t, err)
	require.Contains(t, out, `"restart_scopes"`)
}
func TestRestartErrorsPropagate(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("tenant denied"), errors.New("job not found"), errors.New("restart context unavailable: missing recipe")} {
		p := &restartProbe{failure: failure}
		out, err := executeRestartTest(newRestartCommand(restartTestOpen(p)), "job", "--scope", "x", "--request-id", "key")
		require.ErrorIs(t, err, failure)
		require.Empty(t, out)
		var uri string
		var embed, jsonOut bool
		out, err = executeRestartTest(newRestartInspection("context", &uri, &embed, &jsonOut, restartTestOpen(p)), "job", "--scope", "x")
		require.ErrorIs(t, err, failure)
		require.Empty(t, out)
	}
}
func TestRestartHelpDocumentsContract(t *testing.T) {
	out, err := executeRestartTest(newRestartCmd(), "--help")
	require.NoError(t, err)
	for _, value := range []string{"restart-points", "restart-context", "--patch-file", "Ambiguous", "deferred", "replaces", "retry the same payload", "side effects may repeat"} {
		require.Contains(t, out, value)
	}
}
