package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/selfinfo"
	"github.com/stretchr/testify/require"
)

type cliInspection struct {
	requests []jobinspect.Request
	failure  error
}

func (a *cliInspection) Inspect(_ context.Context, q jobinspect.Request) (*jobinspect.Response, error) {
	a.requests = append(a.requests, q)
	if a.failure != nil {
		return nil, a.failure
	}
	if q.Field != nil {
		return &jobinspect.Response{Version: 1, Value: json.RawMessage(`null`)}, nil
	}
	if q.Operation == "snapshot" {
		return &jobinspect.Response{Version: 1, Snapshot: &jobinspect.Snapshot{Version: 1, JobID: q.JobID}}, nil
	}
	return &jobinspect.Response{Version: 1, Job: &jobinspect.Job{ID: q.JobID, Status: "failed"}, Tasks: []jobinspect.Task{{ID: "native/task#2", Attempt: 2, Status: "failed"}}}, nil
}
func (a *cliInspection) Download(_ context.Context, q jobinspect.Request) (io.ReadCloser, error) {
	a.requests = append(a.requests, q)
	return io.NopCloser(bytes.NewReader([]byte{0, 255, '\n'})), nil
}

func TestJobCLIUsesTaskHelperAndExactOutput(t *testing.T) {
	api := &cliInspection{}
	current := jobcontext.Current{TenantID: "tenant", JobID: "parent", GoalID: "goal"}
	s, e := childbroker.Start(jobinspect.WithAPI(context.Background(), api), childbroker.Options{Current: current, Self: func() selfinfo.Info { return selfinfo.Unknown("") }})
	require.NoError(t, e)
	defer s.Close()
	for k, v := range jobcontext.EnvForCurrent(current) {
		t.Setenv(k, v)
	}
	for k, v := range s.Env() {
		t.Setenv(k, v)
	}
	// A broken ambient destination must never be opened while a task helper exists.
	t.Setenv("C2J_JOBDB", "invalid://ambient")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		c := newJobCmd()
		c.SetOut(&out)
		c.SetErr(io.Discard)
		c.SetArgs(args)
		e := c.Execute()
		return out.String(), e
	}
	out, e := run("log", "self", "--json")
	require.NoError(t, e)
	require.Contains(t, out, `"id": "parent"`)
	require.Equal(t, "parent", api.requests[len(api.requests)-1].JobID)
	out, e = run("log", "child", "--task", "native/task#2", "--attempt", "2", "--field", "/null")
	require.NoError(t, e)
	require.Equal(t, "null\n", out)
	require.Equal(t, 2, api.requests[len(api.requests)-1].Attempt)
	_, e = run("show", "unrelated", "--json")
	require.NoError(t, e)
	require.Equal(t, "unrelated", api.requests[len(api.requests)-1].JobID)
	_, e = run("list", "--parent", "other-parent", "--json")
	require.NoError(t, e)
	require.Equal(t, "other-parent", api.requests[len(api.requests)-1].Parent)
	_, e = run("list", "--help")
	require.NoError(t, e)
	_, e = run("show", "unrelated", "--jobdb", "invalid://retarget")
	require.NoError(t, e)
	file := filepath.Join(t.TempDir(), "log.bin")
	_, e = run("artifact", "child", "--task", "native/task#2", "--name", "check.log", "--output", file)
	require.NoError(t, e)
	raw, e := os.ReadFile(file)
	require.NoError(t, e)
	require.Equal(t, []byte{0, 255, '\n'}, raw)
	_, e = run("artifact", "child", "--name", "check.log", "--output", file)
	require.Error(t, e)
	raw, e = os.ReadFile(file)
	require.NoError(t, e)
	require.Equal(t, []byte{0, 255, '\n'}, raw)
	dir := filepath.Join(t.TempDir(), "recovered")
	out, e = run("snapshot", "child", "--dir", dir)
	require.NoError(t, e)
	require.Contains(t, out, "Snapshot saved")
	require.FileExists(t, filepath.Join(dir, "snapshot.json"))
	_, e = run("snapshot", "child", "--dir", dir, "--output", "elsewhere")
	require.ErrorContains(t, e, "mutually exclusive")
	_, e = run("artifact", "child", "--name", "check.log")
	require.ErrorContains(t, e, "requires --output")
	api.failure = fmt.Errorf("inspection backend denied")
	_, e = run("show", "unrelated", "--jobdb", "embed:///")
	require.ErrorContains(t, e, "inspection backend denied")

}

func TestJobCLIRejectsMissingHelperAndPartialFile(t *testing.T) {
	for k, v := range jobcontext.EnvForCurrent(jobcontext.Current{TenantID: "tenant", JobID: "job", GoalID: "goal"}) {
		t.Setenv(k, v)
	}
	for _, k := range []string{jobcontext.ChildJobEndpointEnv, jobcontext.ChildJobTokenEnv, jobcontext.ChildJobSessionIDEnv} {
		t.Setenv(k, "")
	}
	_, _, _, e := openInspection(context.Background(), "embed:///")
	require.ErrorContains(t, e, "helper unavailable")
	file := filepath.Join(t.TempDir(), "partial")
	e = writeInspectionFile(file, io.Discard, func(w io.Writer) error {
		_, _ = io.Copy(w, strings.NewReader("partial"))
		return fmt.Errorf("download failed")
	})
	require.Error(t, e)
	require.NoFileExists(t, file)
}

func TestJobInspectionHelpDescribesBackendPermissions(t *testing.T) {
	c := newJobCmd()
	require.Contains(t, c.Long, "JobDB permissions")
	for _, child := range c.Commands() {
		if child.Name() == "list" {
			require.Contains(t, child.Short, "--parent JOB")
		}
	}
}
