package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/cmd/c2j/internal/swfruntime"
	"github.com/colony-2/c2j/internal/buildinfo"
	"github.com/colony-2/c2j/internal/localruntime"
	"github.com/colony-2/c2j/pkg/cellregistry"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

func standaloneCellCLI(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "C2J_CURRENT_") || strings.HasPrefix(key, "C2J_CHILD_JOB_") {
			t.Setenv(key, "")
		}
	}
}

func runCellCLI(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	cmd := newRootCmd(buildinfo.Info{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(append([]string{"cell"}, args...))
	err := cmd.ExecuteContext(t.Context())
	return out.Bytes(), err
}

func TestCellLifecycleCLIEmbedded(t *testing.T) {
	standaloneCellCLI(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(defaults.JobDBEnv, "embed:///")
	raw, err := runCellCLI(t, "create", "payments", "--repo", "example.com/team/payments", "--json")
	require.NoError(t, err)
	var created jobdb.Cell
	require.NoError(t, json.Unmarshal(raw, &created))
	ctx := t.Context()
	handle, err := swfruntime.Open(ctx, "embed:///")
	require.NoError(t, err)
	history, err := handle.Runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
		TenantId: created.TenantID, JobType: "fixture", Cell: created.Name, Data: jobdb.NewTaskDataOrPanic(nil),
	}})
	require.NoError(t, err)
	lease, err := handle.Runtime.GetJobLease(ctx, jobdb.GetJobLeaseRequest{
		JobKey: history.JobKey, WorkerID: "fixture-worker", Routes: []jobdb.Route{{JobType: "fixture"}},
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, lease.Complete(ctx, jobdb.CompleteExecutionRequest{Status: "success", Chapter: &jobdb.Chapter{
		Ordinal: 1, TaskType: "fixture", CreatedAt: time.Now(), Body: jobdb.JobAttemptOutcomeChapter{
			Outcome: jobdb.ApplicationOutputOutcome{Output: jobdb.ApplicationOutputBytes{Data: []byte(`1`)}},
		},
	}}))
	require.NoError(t, handle.Cleanup())
	assertState := func(active bool) {
		t.Helper()
		raw, err := runCellCLI(t, "list", "--json")
		require.NoError(t, err)
		var cells []jobdb.Cell
		require.NoError(t, json.Unmarshal(raw, &cells))
		if active {
			require.Len(t, cells, 1)
			require.Equal(t, created.ID, cells[0].ID)
		} else {
			require.Empty(t, cells)
		}
		handle, err := swfruntime.Open(ctx, "embed:///")
		require.NoError(t, err)
		defer handle.Cleanup()
		job, err := handle.Runtime.GetJob(ctx, history.JobKey)
		require.NoError(t, err)
		require.Equal(t, created.ID, job.CellID)
	}
	assertState(true)
	for _, step := range []struct {
		command string
		state   jobdb.CellState
	}{{"archive", jobdb.CellRetired}, {"unarchive", jobdb.CellActive}} {
		raw, err := runCellCLI(t, step.command, created.Name, "--json")
		require.NoError(t, err)
		var changed jobdb.Cell
		require.NoError(t, json.Unmarshal(raw, &changed))
		require.Equal(t, created.ID, changed.ID)
		require.Equal(t, step.state, changed.State)
		assertState(step.state == jobdb.CellActive)
	}
}

func TestCellLifecycleCLIRemote(t *testing.T) {
	standaloneCellCLI(t)
	t.Setenv("JOBDB_BEARER_TOKEN", "fixture-token")
	type lifecycleCase struct {
		name, removeGrant string
		failure           *jobdb.CellError
		requests          int
	}
	for _, step := range []struct {
		command, route, grant string
		want                  jobdb.CellState
	}{
		{"archive", "retire", "cells.retire", jobdb.CellRetired},
		{"unarchive", "restore", "cells.restore", jobdb.CellActive},
	} {
		cases := []lifecycleCase{
			{name: "success", requests: 3},
			{name: "stale", failure: jobdb.ErrCellPrecondition, requests: 3},
			{name: "list denied", removeGrant: "cells.list", failure: jobdb.ErrCellForbidden, requests: 1},
			{name: "get denied", removeGrant: "cells.get", failure: jobdb.ErrCellForbidden, requests: 2},
			{name: "write denied", removeGrant: step.grant, failure: jobdb.ErrCellForbidden, requests: 3},
		}
		if step.command == "unarchive" {
			cases = append(cases,
				lifecycleCase{name: "unconfirmed", requests: 0},
				lifecycleCase{name: "unsupported", failure: jobdb.ErrCellRestoreUnsupported, requests: 3},
				lifecycleCase{name: "routing 404", requests: 3},
				lifecycleCase{name: "structured not-found", failure: jobdb.ErrCellNotFound, requests: 3},
			)
		}
		for _, scenario := range cases {
			t.Run(step.command+"/"+scenario.name, func(t *testing.T) {
				const tenant = "tenant"
				runtime := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
				admin := localruntime.Context(t.Context())
				before, err := runtime.CreateCell(admin, jobdb.CreateCellRequest{TenantID: tenant, Name: "payments", Repo: "example.com/payments"})
				require.NoError(t, err)
				key := jobdb.CellKey{TenantID: tenant, CellID: before.ID}
				if step.command == "unarchive" {
					before, err = runtime.RetireCell(admin, jobdb.RetireCellRequest{CellKey: key, IfMatch: strconv.Quote(before.Revision)})
					require.NoError(t, err)
				}
				expectedRevision := before.Revision
				if scenario.name == "stale" {
					expectedRevision = "older-revision"
				}
				var backend jobdb.ConsumerRuntime = runtime
				if scenario.name == "unsupported" {
					backend = struct {
						jobdb.ConsumerRuntime
						jobdb.ConfigurationAdmin
					}{runtime, runtime}
				}
				handler := remote.NewConsumerServer(backend)
				listPath := "/v1/tenants/" + tenant + "/cells"
				cellPath := listPath + "/" + key.CellID
				lifecyclePath := cellPath + "/" + step.route
				var requests []string
				var requestMu sync.Mutex
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					requestMu.Lock()
					requests = append(requests, req.Method+" "+req.URL.Path)
					requestMu.Unlock()
					if !assertBearer(t, w, req) {
						return
					}
					authority, _ := jobdb.CellAuthorityFromContext(admin)
					authority.TenantID = tenant
					authority.Permissions["cells.restore"] = true
					delete(authority.Permissions, scenario.removeGrant)
					switch req.URL.Path {
					case listPath:
						query := req.URL.Query()
						if query.Get("name") != before.Name || query.Get("state") != "ALL" {
							t.Errorf("expected tenant name lookup with State ALL, got %s", req.URL.RawQuery)
						}
					case cellPath:
						if scenario.name == "stale" {
							stale := before
							stale.Revision = expectedRevision
							w.Header().Set("ETag", strconv.Quote(stale.Revision))
							_ = json.NewEncoder(w).Encode(stale)
							return
						}
					case lifecyclePath:
						if values := req.Header.Values("If-Match"); len(values) != 1 || values[0] != strconv.Quote(expectedRevision) {
							t.Errorf("expected one strong If-Match %q, got %v", strconv.Quote(expectedRevision), values)
						}
						if scenario.name == "routing 404" {
							http.NotFound(w, req)
							return
						}
						if scenario.name == "structured not-found" {
							authority.Cells = map[string]bool{}
						}
					}
					handler.ServeHTTP(w, req.WithContext(jobdb.WithCellAuthority(req.Context(), authority)))
				}))
				defer server.Close()
				args := []string{step.command, before.Name, "--jobdb", server.URL + "/" + tenant, "--json"}
				if step.command == "unarchive" && scenario.name != "unconfirmed" {
					args = append(args, "--restore-supported")
				}
				raw, err := runCellCLI(t, args...)
				server.Close()
				expected := []string{"GET " + listPath, "GET " + cellPath, "POST " + lifecyclePath}
				requestMu.Lock()
				observed := append([]string{}, requests...)
				requestMu.Unlock()
				require.Equal(t, expected[:scenario.requests], observed)
				after, getErr := runtime.GetCell(admin, key)
				require.NoError(t, getErr)
				if scenario.name == "success" {
					require.NoError(t, err)
					var changed jobdb.Cell
					require.NoError(t, json.Unmarshal(raw, &changed))
					require.Equal(t, before.ID, changed.ID)
					require.Equal(t, step.want, changed.State)
					require.Equal(t, changed.State, after.State)
					return
				}
				require.Equal(t, before, after)
				switch scenario.name {
				case "unconfirmed":
					require.ErrorIs(t, err, cellregistry.ErrRestoreSupportUnconfirmed)
					require.ErrorContains(t, err, "--restore-supported")
				case "routing 404":
					require.ErrorIs(t, err, cellregistry.ErrRestoreOutcomeUnconfirmed)
					require.ErrorContains(t, err, "HTTP 404")
					require.ErrorContains(t, err, "verify deployment support and cell state")
					_, _, structured := jobdb.CellErrorStatus(err)
					require.False(t, structured)
				default:
					var typed *jobdb.CellError
					require.ErrorAs(t, err, &typed)
					require.Equal(t, scenario.failure, typed)
					require.NotErrorIs(t, err, cellregistry.ErrRestoreOutcomeUnconfirmed)
					if scenario.name == "stale" {
						require.ErrorContains(t, err, "review the current cell state before retrying")
					}
				}
			})
		}
	}
}

func assertBearer(t *testing.T, w http.ResponseWriter, req *http.Request) bool {
	t.Helper()
	if req.Header.Get("Authorization") != "Bearer fixture-token" {
		t.Error("request did not use the configured bearer credentials")
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func TestCellLifecycleCLIRejectsInvalidNamesAndRunningTasks(t *testing.T) {
	standaloneCellCLI(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		http.NotFound(w, req)
	}))
	defer server.Close()
	for _, operation := range []string{"archive", "unarchive"} {
		t.Run(operation, func(t *testing.T) {
			flags := []string{"--jobdb", server.URL + "/tenant"}
			if operation == "unarchive" {
				flags = append(flags, "--restore-supported")
			}
			_, err := runCellCLI(t, append([]string{operation, "../bad"}, flags...)...)
			require.ErrorContains(t, err, "invalid cell name")
			t.Setenv(jobcontext.CurrentJobIDEnv, "running-job")
			t.Setenv(jobcontext.CurrentTenantIDEnv, "tenant")
			_, err = runCellCLI(t, append([]string{operation, "payments"}, flags...)...)
			require.ErrorContains(t, err, "not available inside a running task")
		})
	}
}
