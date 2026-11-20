package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobinspect"
	coretask "github.com/colony-2/c2j/pkg/task"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type savedCLI struct {
	jobinspect.API
	calls   int
	request jobinspect.WorkspaceDiffRequest
	status  string
	data    []byte
	fault   error
	size    int64
}

func (a *savedCLI) metadata(q jobinspect.WorkspaceDiffRequest) *jobinspect.WorkspaceDiff {
	a.calls++
	a.request = q
	m := &jobinspect.WorkspaceDiff{Version: 1, Status: a.status, Requested: q, Observation: "observed", Cutoff: "cutoff", Resolved: jobinspect.WorkspaceDiffPoint{Task: "task", Attempt: 1, Status: "succeeded", Boundary: "after"}}
	if q.Task != nil {
		m.Resolved.Task = *q.Task
	}
	if q.Scope != nil {
		m.Resolved.Scope = *q.Scope
	}
	if q.Attempt != nil {
		m.Resolved.Attempt = *q.Attempt
	}
	if q.Boundary != nil {
		m.Resolved.Boundary = *q.Boundary
	}
	if a.status == "unavailable" {
		m.Reasons = []string{"not retained"}
		return m
	}
	m.Workspace = &coretask.WorkspaceState{Repository: "example.test/repo", BaseCommit: strings.Repeat("a", 40), HeadCommit: strings.Repeat("b", 40)}
	if q.Workspace != nil && *q.Workspace != "main" {
		m.Workspace.ScopeID = *q.Workspace
	}
	m.Resolved.Workspace = m.Workspace.ScopeID
	m.StateProducer = &jobinspect.WorkspaceDiffProducer{JobID: q.JobID, Task: "task", Attempt: 1}
	m.Source = &jobinspect.WorkspaceDiffSource{Producer: *m.StateProducer, Key: jobdb.ArtifactKey{JobId: q.JobID, Name: "diff_from_base.diff", TaskOrdinal: 3, SizeBytes: a.size}}
	m.Bytes = &a.size
	return m
}
func (a *savedCLI) Inspect(_ context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	return &jobinspect.Response{Version: 1, WorkspaceDiff: a.metadata(jobinspect.WorkspaceDiffRequest{JobID: q.JobID, WorkspaceDiffOptions: *q.WorkspaceDiff})}, nil
}
func (a *savedCLI) OpenWorkspaceDiff(ctx context.Context, q jobinspect.WorkspaceDiffRequest) (*jobinspect.WorkspaceDiff, io.ReadCloser, error) {
	m := a.metadata(q)
	if m.Status == "unavailable" {
		return m, nil, nil
	}
	return m, jobinspect.NewWorkspaceDiffStream(ctx, &cliSavedBody{Reader: bytes.NewReader(a.data), fault: a.fault}, m.Bytes), nil
}

type cliSavedBody struct {
	io.Reader
	fault error
}

func (b *cliSavedBody) Close() error { return b.fault }
func savedCLICommand(t *testing.T, api jobinspect.API, args ...string) ([]byte, []byte, error) {
	t.Helper()
	var out bytes.Buffer
	diag, err := savedCLICommandTo(t, api, &out, args...)
	return out.Bytes(), diag, err
}

func savedCLICommandTo(t *testing.T, api jobinspect.API, out io.Writer, args ...string) ([]byte, error) {
	t.Helper()
	var uri string
	var embed, jsonOutput bool
	var diag bytes.Buffer
	c := &cobra.Command{Use: "job"}
	c.SetOut(out)
	c.SetErr(&diag)
	c.SetArgs(args)
	c.PersistentFlags().BoolVar(&jsonOutput, "json", false, "JSON")
	c.AddCommand(newJobChangesCommands(&uri, &embed, &jsonOutput, func(context.Context, string) (jobinspect.API, func(), string, error) {
		return api, func() {}, "current", nil
	})...)
	err := c.Execute()
	return diag.Bytes(), err
}

func TestWorkspaceDiffCLI_C05K02OutputFailures(t *testing.T) {
	api := &savedCLI{status: "available", data: []byte("saved"), size: 5}
	_, err := savedCLICommandTo(t, api, brokenDAGWriter{}, "diff", "job")
	require.ErrorContains(t, err, "write failed")
	path := filepath.Join(t.TempDir(), "output-close")
	// Exercise the same exclusive writer used by raw diff and metadata modes.
	err = writeInspectionFile(path, io.Discard, func(w io.Writer) error { return w.(*os.File).Close() })
	require.Error(t, err)
	require.NoFileExists(t, path)
}
func TestWorkspaceDiffCLI_C04V04K01Migration(t *testing.T) {
	for _, args := range [][]string{{"--all=false"}, {"--commit="}, {"--from="}, {"--through="}, {"--base="}, {"--selection="}, {"--cursor="}, {"--limit="}, {"--json", "--output", "-"}, {"--json", "--output", "new"}, {"--attempt", "0"}, {"--workspace", ""}, {"--boundary", "before"}, {"--task", "x", "--scope", "y"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			api := &savedCLI{}
			out, _, err := savedCLICommand(t, api, append([]string{"diff", "job"}, args...)...)
			require.Error(t, err)
			require.Empty(t, out)
			require.Zero(t, api.calls)
		})
	}
}
func TestWorkspaceDiffCLI_C01C02C03C05K02Selectors(t *testing.T) {
	api := &savedCLI{status: "available", data: []byte("raw saved bytes | μ\r\n"), size: int64(len([]byte("raw saved bytes | μ\r\n")))}
	out, diag, err := savedCLICommand(t, api, "diff", "self", "--task", "native/μ", "--attempt", "2", "--workspace", "root", "--boundary", "before")
	require.NoError(t, err)
	require.Empty(t, diag)
	require.Equal(t, api.data, out)
	require.Equal(t, "current", api.request.JobID)
	require.Equal(t, "native/μ", *api.request.Task)
	require.Equal(t, 2, *api.request.Attempt)
	require.Equal(t, "root", *api.request.Workspace)
	require.Equal(t, "before", *api.request.Boundary)
	path := filepath.Join(t.TempDir(), "patch")
	_, _, err = savedCLICommand(t, api, "diff", "job", "--output", path)
	require.NoError(t, err)
	_, _, err = savedCLICommand(t, api, "diff", "job", "--output", path)
	require.Error(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, api.data, data)
	for _, status := range []string{"available", "empty", "unavailable"} {
		t.Run(status, func(t *testing.T) {
			api.status = status
			if status == "empty" {
				api.size = 0
				api.data = nil
			}
			out, _, err := savedCLICommand(t, api, "diff", "job", "--json")
			if status == "unavailable" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var m jobinspect.WorkspaceDiff
			require.NoError(t, json.Unmarshal(out, &m))
			require.Equal(t, status, m.Status)
			out, diag, err := savedCLICommand(t, api, "changes", "job", "--scope", "scope")
			require.Contains(t, string(diag), "deprecated")
			if status == "unavailable" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, json.Unmarshal(out, &m))
			require.Equal(t, "scope", *api.request.Scope)
		})
	}
	for _, fault := range []string{"short", "extra", "close"} {
		t.Run(fault, func(t *testing.T) {
			a := &savedCLI{status: "available", data: []byte("bytes"), size: 5}
			switch fault {
			case "short":
				a.size = 6
			case "extra":
				a.size = 4
			case "close":
				a.fault = errors.New("close failed")
			}
			path := filepath.Join(t.TempDir(), "partial")
			_, _, err := savedCLICommand(t, a, "diff", "job", "--output", path)
			require.Error(t, err)
			_, err = os.Stat(path)
			require.True(t, os.IsNotExist(err))
		})
	}
}
