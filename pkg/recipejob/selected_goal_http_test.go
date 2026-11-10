package recipejob_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/colony-2/jobdb/pkg/jobdb/runtimetest"
	"github.com/stretchr/testify/require"
)

func TestSelectedGoalHTTPUnavailableAndIncomplete(t *testing.T) {
	for _, mode := range []string{"legacy", "unknown", "removed", "redirect", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			var ordinary atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/capabilities/primary-goal-selection" {
					if mode == "legacy" {
						http.NotFound(w, r)
						return
					}
					if mode == "unknown" {
						w.Write([]byte(`{}`))
						return
					}
					w.Write([]byte(`{"supported":true}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/primary-goal") {
					switch mode {
					case "removed":
						http.NotFound(w, r)
					case "redirect":
						w.Header().Set("Location", "/v1/tenants/tenant/jobs/dest")
						w.WriteHeader(307)
					default:
						w.Write([]byte(`{"tenantId":"tenant","jobId":"dest","goalId":"goal"}`))
					}
					return
				}
				if r.Method == http.MethodPost || r.Method == http.MethodPut {
					ordinary.Add(1)
				}
				w.Write([]byte(`{}`))
			}))
			defer server.Close()
			rt, err := remote.New(server.URL, server.Client())
			require.NoError(t, err)
			engine := goalEngine(t, rt)
			_, err = starter.StartRecipeJob(context.Background(), workflowctl.StartJob{TenantId: "tenant", JobID: "dest", GoalID: "goal", Cell: "work", CellRepo: "example.test/work", RecipeName: "build"}, engine)
			if mode == "incomplete" {
				require.ErrorIs(t, err, jobdb.ErrSubmissionReceiptUnavailable)
			} else {
				require.ErrorIs(t, err, jobdb.ErrPrimaryGoalSelectionUnsupported)
			}
			require.Zero(t, ordinary.Load())
		})
	}
}
func TestSelectedGoalHTTPRevokedReplay(t *testing.T) {
	ctx := runtimetest.PrimaryGoalContext(context.Background())
	rt := toy.New()
	celltest.Create(t, rt, "tenant", "work", "example.test/work")
	source, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "source", JobType: "work", Cell: "work", CellRepo: "example.test/work", Data: jobdb.NewTaskDataOrPanic(nil)}})
	require.NoError(t, err)
	var revoked atomic.Bool
	handler := remote.NewServer(rt)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trusted := jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(r.Context()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error {
			if revoked.Load() {
				return jobdb.ErrPrimaryGoalSelectionForbidden
			}
			return nil
		})
		handler.ServeHTTP(w, r.WithContext(trusted))
	}))
	defer server.Close()
	client, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	engine := goalEngine(t, client)
	job, err := starter.PrepareRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "dest", GoalID: source.GoalID, Cell: "work", CellRepo: "example.test/work", RecipeName: "build"}, starter.StartRecipeJobOptions{})
	require.NoError(t, err)
	d, err := starter.NewDescriptor(ctx, job)
	require.NoError(t, err)
	first, err := d.Submit(ctx, engine)
	require.NoError(t, err)
	revoked.Store(true)
	_, err = d.Submit(ctx, engine)
	require.ErrorIs(t, err, jobdb.ErrPrimaryGoalSelectionForbidden)
	job.JobID = "unpublished"
	unpublished, err := starter.NewDescriptor(ctx, job)
	require.NoError(t, err)
	_, err = unpublished.Submit(ctx, engine)
	require.ErrorIs(t, err, jobdb.ErrPrimaryGoalSelectionForbidden)
	_, err = rt.GetJob(ctx, jobdb.JobKey{TenantId: "tenant", JobId: "unpublished"})
	require.ErrorIs(t, err, jobdb.ErrJobNotFound)
	revoked.Store(false)
	again, err := d.Submit(ctx, engine)
	require.NoError(t, err)
	require.Equal(t, first, again)
}

func TestSelectedGoalLegacyHTTPPreservesOrdinary(t *testing.T) {
	ctx := runtimetest.PrimaryGoalContext(context.Background())
	raw := toy.New()
	celltest.Create(t, raw, "tenant", "work", "example.test/work")
	handler := remote.NewServer(raw)
	var selectedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/capabilities/primary-goal-selection" || strings.HasSuffix(r.URL.Path, "/primary-goal") {
			selectedCalls.Add(1)
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r.WithContext(celltest.Context(r.Context())))
	}))
	defer server.Close()
	client, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	engine := goalEngine(t, client)
	ref, err := starter.StartRecipeJob(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "ordinary", Cell: "work", CellRepo: "example.test/work", RecipeName: "build"}, engine)
	require.NoError(t, err)
	require.NotEmpty(t, ref.GoalID)
	require.Equal(t, "ordinary", ref.JobID)
	require.Zero(t, selectedCalls.Load())
}
