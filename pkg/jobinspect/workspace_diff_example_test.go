package jobinspect_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/colony-2/c2j/pkg/childbroker"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/workspacedifftest"
	"github.com/stretchr/testify/require"
)

var _ jobinspect.WorkspaceDiffAPI = (*jobinspect.Service)(nil)
var _ jobinspect.WorkspaceDiffAPI = childbroker.JobClient{}

func ExampleWorkspaceDiffRequest_latest() {
	q := jobinspect.WorkspaceDiffRequest{JobID: "JOB"}
	raw, _ := json.Marshal(q)
	fmt.Println(string(raw))
	// Output: {"job_id":"JOB"}
}

func ExampleWorkspaceDiffRequest_selected() {
	task := `/scope:"$job"#1/attempt:1/task:"step"#2`
	attempt, workspace, boundary := 1, "main", "before"
	q := jobinspect.WorkspaceDiffRequest{JobID: "JOB", WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: &task, Attempt: &attempt, Workspace: &workspace, Boundary: &boundary}}
	raw, _ := json.Marshal(q)
	fmt.Println(string(raw))
	// Output: {"job_id":"JOB","task":"/scope:\"$job\"#1/attempt:1/task:\"step\"#2","attempt":1,"workspace":"main","boundary":"before"}
}

// copyWorkspaceDiff works with Service and authenticated JobClient. The caller
// owns destination cleanup if copying or source Close fails. Unknown Bytes must
// not be treated as zero, and copying a known count alone cannot certify EOF.
func copyWorkspaceDiff(ctx context.Context, api jobinspect.WorkspaceDiffAPI, q jobinspect.WorkspaceDiffRequest, dst io.Writer) (*jobinspect.WorkspaceDiff, error) {
	m, body, err := api.OpenWorkspaceDiff(ctx, q)
	if err != nil {
		return nil, err
	}
	if m.Status == "unavailable" {
		return m, fmt.Errorf("saved comparison unavailable: %v", m.Reasons)
	}
	_, copyErr := io.Copy(dst, body)
	return m, errors.Join(copyErr, body.Close())
}

func TestWorkspaceDiffExamplesCopy(t *testing.T) {
	ctx, rt := savedBackend(t, "SQLite")
	f, err := workspacedifftest.Build(ctx, rt, t.TempDir())
	require.NoError(t, err)
	service := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
	q := jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId}
	m, err := copyWorkspaceDiff(ctx, service, q, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "available", m.Status)
	log, err := service.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
	require.NoError(t, err)
	q.Task, q.Boundary = strp(log.Tasks[0].ID), strp("before")
	m, err = copyWorkspaceDiff(ctx, service, q, io.Discard)
	require.NoError(t, err)
	require.Equal(t, "empty", m.Status)
	require.Nil(t, m.Source)
	missing, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{Transform: func(s []workspacedifftest.Step) []workspacedifftest.Step { s[2].Omit = true; return s }})
	require.NoError(t, err)
	m, err = copyWorkspaceDiff(ctx, service, jobinspect.WorkspaceDiffRequest{JobID: missing.Job.JobId}, io.Discard)
	require.Error(t, err)
	require.Equal(t, "unavailable", m.Status)
	for _, fault := range []string{"read", "close"} {
		service.Runtime = &savedFaultRuntime{Runtime: rt, kind: fault}
		_, err = copyWorkspaceDiff(ctx, service, jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId}, io.Discard)
		var typed *jobinspect.WorkspaceDiffError
		require.ErrorAs(t, err, &typed)
		require.Equal(t, fault, typed.Code)
	}
}

// These complete JSON examples are emitted from native persisted fixtures, then
// decoded and validated just like consumer metadata. They contain no invented
// hashes, observation IDs or artifact keys.
func TestWorkspaceDiffExamplesMetadata(t *testing.T) {
	ctx, rt := savedBackend(t, "SQLite")
	f, err := workspacedifftest.Build(ctx, rt, t.TempDir())
	require.NoError(t, err)
	service := &jobinspect.Service{Runtime: rt, Tenant: f.Job.TenantId}
	log, err := service.Inspect(ctx, jobinspect.Request{Operation: "log", JobID: f.Job.JobId})
	require.NoError(t, err)
	empty, err := workspacedifftest.BuildWithOptions(ctx, rt, t.TempDir(), workspacedifftest.Options{EmptyComparison: true})
	require.NoError(t, err)
	for _, example := range []struct {
		name    string
		request jobinspect.WorkspaceDiffRequest
	}{
		{"available", jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId}},
		{"empty-without-source", jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: strp(log.Tasks[0].ID), Boundary: strp("before")}}},
		{"empty-retained", jobinspect.WorkspaceDiffRequest{JobID: empty.Job.JobId}},
		{"unavailable", jobinspect.WorkspaceDiffRequest{JobID: f.Job.JobId, WorkspaceDiffOptions: jobinspect.WorkspaceDiffOptions{Task: strp(log.Tasks[0].ID)}}},
	} {
		t.Run(example.name, func(t *testing.T) {
			m, body, err := service.OpenWorkspaceDiff(ctx, example.request)
			require.NoError(t, err)
			if body != nil {
				_, err = io.Copy(io.Discard, body)
				require.NoError(t, err)
				require.NoError(t, body.Close())
			}
			raw, err := json.Marshal(m)
			require.NoError(t, err)
			var decoded jobinspect.WorkspaceDiff
			require.NoError(t, json.Unmarshal(raw, &decoded))
			require.NoError(t, jobinspect.ValidateWorkspaceDiff(example.request, &decoded))
			t.Logf("NATIVE_METADATA %s %s", example.name, raw)
		})
	}
}
