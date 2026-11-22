package runjob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
)

type provenanceReaderFunc func(context.Context, jobdb.ReadExecutionProvenanceRequest) (jobdb.ExecutionProvenance, error)

func (f provenanceReaderFunc) ReadExecutionProvenance(ctx context.Context, r jobdb.ReadExecutionProvenanceRequest) (jobdb.ExecutionProvenance, error) {
	return f(ctx, r)
}

func TestExecutionDiagnosticPairsAndSource(t *testing.T) {
	zero, ceiling := int64(0), int64(29)
	c1, c2 := "commit-one", "commit-two"
	source := jobdb.JobKey{TenantId: "tenant", JobId: "prerequisite"}
	original := errors.New("private durable body and credential must not be printed")
	failure := jobdb.ClassifyExecutionInfrastructure(original, jobdb.ExecutionInfrastructureDurableDecode, "c2j.submission", &source, &zero)
	reader := provenanceReaderFunc(func(ctx context.Context, req jobdb.ReadExecutionProvenanceRequest) (jobdb.ExecutionProvenance, error) {
		require.Equal(t, source, req.JobKey)
		require.Equal(t, "c2j", req.Consumer)
		return jobdb.ExecutionProvenance{Capability: jobdb.ExecutionProvenanceCapability, Scope: jobdb.ExecutionProvenanceScope, Consumer: "c2j", Status: jobdb.ExecutionProvenancePartial, ThroughOrdinal: &ceiling,
			Pairs:  []jobdb.ExecutionProvenancePair{{ConsumerVersion: "recorded", ConsumerCommit: &c1, JobDBVersion: "db-one"}, {ConsumerVersion: "recorded", ConsumerCommit: &c2, JobDBVersion: "db-two"}, {ConsumerVersion: "recorded", ConsumerCommit: &c1, JobDBVersion: "db-one"}},
			Issues: []jobdb.ExecutionProvenanceIssue{{Reason: jobdb.ExecutionProvenanceByteLimit}}, OmittedIssueCount: 3, OmittedPairCount: 4}, nil
	})
	d := makeExecutionDiagnostic(context.Background(), reader, jobdb.JobKey{JobId: "caller"}, failure, time.Second)
	require.Equal(t, "prerequisite", d.JobID)
	require.Equal(t, &zero, d.Ordinal)
	require.Len(t, d.Writers, 2)
	require.Equal(t, workerVersions{C2J: "recorded", Commit: &c1, JobDB: "db-one"}, d.Writers[0])
	require.Equal(t, workerVersions{C2J: "recorded", Commit: &c2, JobDB: "db-two"}, d.Writers[1])
	require.Equal(t, jobdb.ExecutionProvenancePartial, d.WritersStatus)
	require.Equal(t, &ceiling, d.ThroughOrdinal)
	require.Equal(t, 3, d.OmittedIssues)
	require.Equal(t, 4, d.OmittedPairs)
	var stderr bytes.Buffer
	opts := Options{TenantID: "tenant", JobID: "caller", Stderr: &stderr}
	err := reportExecutionFailure(context.Background(), opts, reader, exitError{code: 1, err: failure})
	require.ErrorIs(t, err, original)
	var printed interface{ DiagnosticPrinted() bool }
	require.ErrorAs(t, err, &printed)
	require.True(t, printed.DiagnosticPrinted())
	var got executionDiagnostic
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &got))
	require.NotContains(t, stderr.String(), original.Error())
	require.Equal(t, 1, bytes.Count(stderr.Bytes(), []byte("\n")))
	appErr := errors.New("application failed")
	stderr.Reset()
	require.Same(t, appErr, reportExecutionFailure(context.Background(), opts, reader, appErr))
	require.Empty(t, stderr.String())
}

func TestExecutionDiagnosticLookupFailuresStaySecondary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime any
		want    string
	}{
		{"old runtime", struct{}{}, "unsupported"},
		{"denied", provenanceReaderFunc(func(context.Context, jobdb.ReadExecutionProvenanceRequest) (jobdb.ExecutionProvenance, error) {
			return jobdb.ExecutionProvenance{}, jobdb.ErrLeasePermissionDenied
		}), "permission_denied"},
		{"storage", provenanceReaderFunc(func(context.Context, jobdb.ReadExecutionProvenanceRequest) (jobdb.ExecutionProvenance, error) {
			return jobdb.ExecutionProvenance{}, errors.New("secret storage error")
		}), "lookup_failed"},
		{"timeout", provenanceReaderFunc(func(ctx context.Context, _ jobdb.ReadExecutionProvenanceRequest) (jobdb.ExecutionProvenance, error) {
			<-ctx.Done()
			return jobdb.ExecutionProvenance{}, ctx.Err()
		}), "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			d := makeExecutionDiagnostic(context.Background(), tc.runtime, jobdb.JobKey{TenantId: "t", JobId: "j"}, jobdb.ErrWorkflowNotDeterministic, 20*time.Millisecond)
			require.Less(t, time.Since(start), time.Second)
			require.Equal(t, tc.want, d.LookupFailure)
			require.Equal(t, jobdb.ExecutionProvenanceUnavailable, d.WritersStatus)
			require.Empty(t, d.Writers)
			require.Nil(t, d.Ordinal)
			require.Equal(t, "workflow was not deterministic", d.Message)
		})
	}
	// An embedding wrapper must preserve unsupported capability instead of silently dropping it.
	rt := executionruntime.New(nil, execution.Allocation{})
	_, err := rt.ReadExecutionProvenance(context.Background(), jobdb.ReadExecutionProvenanceRequest{})
	require.ErrorIs(t, err, jobdb.ErrExecutionProvenanceUnsupported)
}

type failedDiagnosticWriter struct{}

func (failedDiagnosticWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestExecutionDiagnosticWriteFailureRetainsCause(t *testing.T) {
	original := jobdb.ErrWorkflowNotDeterministic
	err := reportExecutionFailure(context.Background(), Options{Stderr: failedDiagnosticWriter{}}, nil, original)
	require.Same(t, original, err)
}

func TestExecutionDiagnosticHTTPBoundsAndUnavailableReader(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"unsupported", 501, `{"code":"execution_provenance_unsupported"}`, "unsupported"},
		{"older server", 404, "unknown endpoint with private response", "lookup_failed"},
		{"denied", 403, "private authorization response", "permission_denied"},
		{"oversized", 200, strings.Repeat("private-response", 20000), "lookup_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.True(t, strings.HasSuffix(r.URL.Path, "/execution-provenance"), "must not fall back to a full chapter read")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			rt, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			var stderr bytes.Buffer
			err = reportExecutionFailure(context.Background(), Options{JobID: "job", TenantID: "tenant", Stderr: &stderr}, rt, jobdb.ErrWorkflowNotDeterministic)
			require.ErrorIs(t, err, jobdb.ErrWorkflowNotDeterministic)
			var d executionDiagnostic
			require.NoError(t, json.Unmarshal(stderr.Bytes(), &d))
			require.Equal(t, jobdb.ExecutionProvenanceUnavailable, d.WritersStatus)
			require.Equal(t, tc.reason, d.LookupFailure)
			require.NotContains(t, stderr.String(), "private")
			require.Less(t, stderr.Len(), 4096)
		})
	}
}
