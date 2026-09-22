package joblist_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/joblist"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

// Every method except ListJobs is deliberately absent: opening/listing must not
// register schemas, claim work, or initialize any execution machinery.
type readOnlyRuntime struct {
	jobdb.WorkflowRuntime
	list func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)
}

func (r readOnlyRuntime) ListJobs(ctx context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
	return r.list(ctx, req)
}

func serve(t *testing.T, list func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(remote.NewServer(readOnlyRuntime{list: list}))
	t.Cleanup(s.Close)
	return s
}

func client(t *testing.T, s *httptest.Server, tenant string) *joblist.Client {
	t.Helper()
	c, err := joblist.New(joblist.Config{JobDBURI: s.URL + "/" + tenant, HTTPClient: s.Client()})
	require.NoError(t, err)
	return c
}

func demandJob(t *testing.T, id, memory string) jobdb.JobSummary {
	t.Helper()
	d, err := execution.Initial(nil, "pinned", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	meta, err := json.Marshal(map[string]any{"repo": "https://github.com/acme/app.git", "execution": d})
	require.NoError(t, err)
	return jobdb.JobSummary{
		JobKey: jobdb.JobKey{TenantId: "tenant", JobId: id}, JobType: "recipe", Status: jobdb.JobStatusReady,
		Metadata: meta, CreatedAt: time.Unix(100, 0).UTC(), AvailableAt: time.Unix(200, 0).UTC(),
	}
}

func TestRemoteProjectionAndWaitingOnlyExecution(t *testing.T) {
	job := demandJob(t, "job", "2Gi")
	memory := "8Gi"
	d, err := execution.Initial(nil, "pinned", execution.Requirements{Resources: execution.Resources{Memory: &memory}})
	require.NoError(t, err)
	d.Revision = 3
	job.ClientPayload, err = execution.PayloadWithDemand(nil, d)
	require.NoError(t, err)
	job.ClientPayloadRevision = 7
	job.NextRoute = &jobdb.Route{JobType: "recipe", TaskType: "recipe:two-step:next"}
	job.CancelRequested = true
	for _, status := range []jobdb.JobStatus{jobdb.JobStatusReady, jobdb.JobStatusCrashConcern, jobdb.JobStatusActive, jobdb.JobStatusCompleted, jobdb.JobStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			item := job
			item.Status = status
			s := serve(t, func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
				if len(req.Statuses) != 1 || req.Statuses[0] != status {
					return jobdb.ListJobsResponse{}, fmt.Errorf("wrong status filter")
				}
				return jobdb.ListJobsResponse{Jobs: []jobdb.JobSummary{item}}, nil
			})
			page, err := client(t, s, "tenant").List(context.Background(), joblist.Query{Repository: "github.com/acme/app", Statuses: []jobdb.JobStatus{status}})
			require.NoError(t, err)
			require.Len(t, page.Jobs, 1)
			got := page.Jobs[0]
			require.Equal(t, "tenant", got.TenantID)
			require.Equal(t, "job", got.JobID)
			require.Equal(t, "https://github.com/acme/app.git", got.RepositorySource)
			require.Equal(t, job.NextRoute, got.NextRoute)
			require.Equal(t, job.AvailableAt, got.AvailableAt)
			require.True(t, got.CancelRequested)
			require.Equal(t, int64(7), got.ClientPayloadRevision)
			if status == jobdb.JobStatusReady || status == jobdb.JobStatusCrashConcern {
				require.Equal(t, "specified", got.Execution.Status)
				require.Equal(t, "yield", got.Execution.Source)
				require.Equal(t, "8Gi", *got.Execution.Demand.Effective.Resources.Memory)
				require.Equal(t, uint64(3), got.Execution.Demand.Revision)
			} else {
				require.Nil(t, got.Execution.Demand)
				require.Nil(t, got.Execution.Initial)
				require.Equal(t, "unavailable", got.Execution.Source)
				want := "not_waiting"
				if status == jobdb.JobStatusActive {
					want = "in_flight"
				}
				require.Equal(t, want, got.Execution.Status)
			}
		})
	}
}

func TestExecutionDiagnosticsAndUnspecified(t *testing.T) {
	empty, err := execution.Initial(nil, "pinned", execution.Requirements{})
	require.NoError(t, err)
	meta, err := json.Marshal(map[string]any{"execution": empty})
	require.NoError(t, err)
	jobs := []jobdb.JobSummary{
		{Status: jobdb.JobStatusReady, Metadata: meta},
		{Status: jobdb.JobStatusReady},
		{Status: jobdb.JobStatusReady, Metadata: json.RawMessage(`{"execution":{"schema_version":99}}`)},
		{Status: jobdb.JobStatusReady, ClientPayload: json.RawMessage(`{"c2j":{"execution":[]}}`)},
	}
	for i := range jobs {
		jobs[i].JobKey = jobdb.JobKey{TenantId: "tenant", JobId: strconv.Itoa(i)}
		jobs[i].JobType = "recipe"
	}
	s := serve(t, func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		return jobdb.ListJobsResponse{Jobs: jobs}, nil
	})
	c := client(t, s, "tenant")
	page, err := c.List(context.Background(), joblist.Query{Repository: "github.com/acme/app"})
	require.NoError(t, err)
	for i, status := range []string{"unspecified", "unresolved", "unsupported", "malformed"} {
		require.Equal(t, status, page.Jobs[i].Execution.Status)
		if i >= 2 {
			require.NotEmpty(t, page.Jobs[i].Execution.Diagnostic)
		}
	}
	memory := "4Gi"
	_, err = c.List(context.Background(), joblist.Query{Repository: "github.com/acme/app", ExecutionFilter: &execution.Filter{Allocation: execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &memory}}}})
	var demandErr *joblist.ExecutionDemandError
	require.ErrorAs(t, err, &demandErr)
	require.Equal(t, "2", demandErr.JobKey.JobId)
}

func TestRemoteSparsePagination(t *testing.T) {
	jobs := []jobdb.JobSummary{demandJob(t, "1", "8Gi"), demandJob(t, "2", "2Gi"), demandJob(t, "3", "8Gi"), demandJob(t, "4", "2Gi"), demandJob(t, "5", "2Gi")}
	var calls atomic.Int32
	s := serve(t, func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		calls.Add(1)
		start := 0
		if req.PageToken != "" {
			var err error
			start, err = strconv.Atoi(req.PageToken)
			if err != nil {
				return jobdb.ListJobsResponse{}, err
			}
		}
		end := min(start+req.PageSize, len(jobs))
		page := jobdb.ListJobsResponse{Jobs: jobs[start:end]}
		if end < len(jobs) {
			page.NextPageToken = strconv.Itoa(end)
		}
		return page, nil
	})
	c := client(t, s, "tenant")
	memory := "4Gi"
	q := joblist.Query{Repository: "github.com/acme/app", PageSize: 2, ExecutionFilter: &execution.Filter{Allocation: execution.Allocation{SchemaVersion: 1, Resources: execution.Resources{Memory: &memory}}}}
	first, err := c.List(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, []string{"2", "4"}, []string{first.Jobs[0].JobID, first.Jobs[1].JobID})
	require.Greater(t, calls.Load(), int32(1))
	q.PageToken = first.NextPageToken
	last, err := c.List(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, last.Jobs, 1)
	require.Equal(t, "5", last.Jobs[0].JobID)
	require.Empty(t, last.NextPageToken)
	q.ExecutionFilter, q.PageToken = nil, ""
	calls.Store(0)
	page, err := c.List(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, page.Jobs, 2)
	require.Equal(t, int32(1), calls.Load(), "unfiltered listing must fetch only one backend page")
}

func TestConcurrentTenantRepositoryIsolationWithRealBackend(t *testing.T) {
	runtime := toy.New()
	for _, tenant := range []string{"one", "two"} {
		for _, repo := range []string{"app", "other"} {
			for i := 0; i < 3; i++ {
				data, err := jobdb.NewTaskData(map[string]any{})
				require.NoError(t, err)
				meta, err := json.Marshal(map[string]string{"repo": "https://github.com/acme/" + repo + ".git"})
				require.NoError(t, err)
				_, err = runtime.SubmitJob(context.Background(), jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: tenant, JobID: repo + strconv.Itoa(i), JobType: "recipe", Metadata: meta, Data: data, RunPolicy: jobdb.DefaultRunPolicy()}})
				require.NoError(t, err)
			}
		}
	}
	// Only ListJobs is forwarded after fixture creation. Other runtime calls fail.
	s := serve(t, runtime.ListJobs)
	clients := map[string]*joblist.Client{"one": client(t, s, "one"), "two": client(t, s, "two")}
	var wg sync.WaitGroup
	for tenant, c := range clients {
		for _, repo := range []string{"app", "other"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				q := joblist.Query{Repository: "github.com/acme/" + repo, PageSize: 1, JobTypes: []string{"recipe"}, Statuses: []jobdb.JobStatus{jobdb.JobStatusReady}}
				seen := map[string]bool{}
				for {
					page, err := c.List(context.Background(), q)
					if !assertNoError(t, err) {
						return
					}
					for _, item := range page.Jobs {
						if item.TenantID != tenant || item.RepositorySource != "https://github.com/acme/"+repo+".git" || seen[item.JobID] {
							t.Errorf("isolation/pagination violation: %+v", item)
							return
						}
						seen[item.JobID] = true
					}
					if page.NextPageToken == "" {
						break
					}
					q.PageToken = page.NextPageToken
				}
				if len(seen) != 3 {
					t.Errorf("got %d jobs, want 3", len(seen))
				}
			}()
		}
	}
	wg.Wait()
	c := clients["one"]
	page, err := c.List(context.Background(), joblist.Query{Repository: "github.com/acme/missing"})
	require.NoError(t, err)
	require.NotNil(t, page.Jobs)
	require.Empty(t, page.Jobs)
	_, err = c.List(context.Background(), joblist.Query{Repository: "github.com/acme/app", PageToken: "invalid-token"})
	require.Error(t, err)
}

func assertNoError(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Error(err)
		return false
	}
	return true
}

func TestExplicitInputsIgnoreProjectAndEnvironment(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".c2j"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".c2j", "config.yaml"), []byte("invalid: ["), 0600))
	child := filepath.Join(root, "child")
	require.NoError(t, os.MkdirAll(filepath.Join(child, ".c2j"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(child, ".c2j", "config.yaml"), []byte("jobdb: https://wrong.invalid/wrong\nself:\n  repo: github.com/wrong/repo\n"), 0600))
	// A path resembling a canonical remote must not shadow the explicit identity.
	require.NoError(t, os.MkdirAll(filepath.Join(child, "github.com", "acme", "app"), 0700))
	t.Chdir(child)
	t.Setenv("PATH", "")
	t.Setenv("C2J_JOBDB", "https://wrong.invalid/wrong")
	s := serve(t, func(_ context.Context, req jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		pred, err := jobdb.MetadataPredicates(req.MetadataFilter)
		if err != nil {
			return jobdb.ListJobsResponse{}, err
		}
		if req.TenantIds[0] != "tenant" || len(pred) != 1 || pred[0].Values[0] != "https://github.com/acme/app.git" {
			return jobdb.ListJobsResponse{}, fmt.Errorf("wrong explicit scope: %+v", req)
		}
		return jobdb.ListJobsResponse{}, nil
	})
	c := client(t, s, "tenant")
	for _, dir := range []string{child, root} {
		require.NoError(t, os.Chdir(dir))
		page, err := c.List(context.Background(), joblist.Query{Repository: "github.com/acme/app"})
		require.NoError(t, err)
		require.Empty(t, page.Jobs)
	}
	require.Equal(t, "https://wrong.invalid/wrong", os.Getenv("C2J_JOBDB"))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAuthenticationRemoteErrorsAndCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	t.Cleanup(s.Close)
	q := joblist.Query{Repository: "github.com/acme/app"}
	_, err := client(t, s, "tenant").List(context.Background(), q)
	require.ErrorContains(t, err, "401")
	base := s.Client().Transport
	auth := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer test")
		return base.RoundTrip(r)
	})}
	c, err := joblist.New(joblist.Config{JobDBURI: s.URL + "/tenant", HTTPClient: auth})
	require.NoError(t, err)
	_, err = c.List(context.Background(), q)
	require.NoError(t, err)
	for _, deadline := range []bool{false, true} {
		started := make(chan struct{})
		cancelClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}
		c, err := joblist.New(joblist.Config{JobDBURI: s.URL + "/tenant", HTTPClient: cancelClient})
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			want = context.DeadlineExceeded
		}
		if !deadline {
			go func() { <-started; cancel() }()
		}
		_, err = c.List(ctx, q)
		cancel()
		require.ErrorIs(t, err, want)
	}
	connectionErr := errors.New("connection unavailable")
	c, err = joblist.New(joblist.Config{JobDBURI: s.URL + "/tenant", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, connectionErr })}})
	require.NoError(t, err)
	_, err = c.List(context.Background(), q)
	require.ErrorIs(t, err, connectionErr)
}

func TestValidationAndEmptyContinuation(t *testing.T) {
	for _, uri := range []string{"", "embed:///", "http://host", "https://host/one/two", "https://user:pass@host/tenant", "https://host/tenant?query=1", "https://host/tenant#fragment"} {
		_, err := joblist.New(joblist.Config{JobDBURI: uri})
		require.ErrorIs(t, err, joblist.ErrInvalidInput, uri)
	}
	var calls atomic.Int32
	s := serve(t, func(context.Context, jobdb.ListJobsRequest) (jobdb.ListJobsResponse, error) {
		calls.Add(1)
		return jobdb.ListJobsResponse{NextPageToken: "continue"}, nil
	})
	c := client(t, s, "tenant")
	for _, q := range []joblist.Query{
		{}, {Repository: "short-name"}, {Repository: "./local"},
		{Repository: "github.com/acme/app", PageSize: -1},
		{Repository: "github.com/acme/app", Statuses: []jobdb.JobStatus{"WRONG"}},
		{Repository: "github.com/acme/app", JobTypes: []string{""}},
		{Repository: "github.com/acme/app", ExecutionFilter: &execution.Filter{}},
		{Repository: "github.com/acme/app", JobTasks: []jobdb.JobTaskFilter{{JobType: "recipe"}}},
	} {
		_, err := c.List(context.Background(), q)
		require.ErrorIs(t, err, joblist.ErrInvalidInput)
	}
	require.Zero(t, calls.Load())
	q := joblist.Query{Repository: "github.com/acme/app"}
	page, err := c.List(context.Background(), q)
	require.NoError(t, err)
	require.Empty(t, page.Jobs)
	require.Equal(t, "continue", page.NextPageToken)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.List(ctx, q)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), calls.Load())
}
