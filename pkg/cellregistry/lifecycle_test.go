package cellregistry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/cellregistry"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

var confirmedRestore = cellregistry.UnarchiveOptions{RestoreSupportConfirmed: true}

func TestSQLiteCellLifecycle(t *testing.T) {
	ctx := context.Background()
	runtime, err := sqlite.NewFromConfig(ctx, sqlite.Config{
		DBPath: filepath.Join(t.TempDir(), "jobdb.sqlite"), Projects: celltest.UnrestrictedProjects{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Close(ctx)) })
	const tenant = "tenant"
	authority := jobdb.CellAuthority{Principal: "test-admin", TenantID: tenant, Permissions: map[string]bool{
		"cells.create": true, "cells.get": true, "cells.list": true, "cells.retire": true, "cells.restore": true,
		"jobs.submit": true, "jobs.list": true, "jobs.acquire": true,
	}}
	admin := jobdb.WithCellAuthority(ctx, authority)
	created, err := runtime.CreateCell(admin, jobdb.CreateCellRequest{TenantID: tenant, Name: "payments", Repo: "example.com/team/payments"})
	require.NoError(t, err)
	key := jobdb.CellKey{TenantID: tenant, CellID: created.ID}
	history, err := runtime.SubmitJob(admin, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{
		TenantId: tenant, JobType: "fixture", Cell: created.Name, Data: jobdb.NewTaskDataOrPanic(nil),
	}})
	require.NoError(t, err)
	lease, err := runtime.GetJobLease(admin, jobdb.GetJobLeaseRequest{
		JobKey: history.JobKey, WorkerID: "fixture-worker", Routes: []jobdb.Route{{JobType: "fixture"}},
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.NoError(t, lease.Complete(admin, jobdb.CompleteExecutionRequest{Status: "success", Chapter: &jobdb.Chapter{
		Ordinal: 1, TaskType: "fixture", CreatedAt: time.Now(), Body: jobdb.JobAttemptOutcomeChapter{
			Outcome: jobdb.ApplicationOutputOutcome{Output: jobdb.ApplicationOutputBytes{Data: []byte(`1`)}},
		},
	}}))
	assertState := func(want jobdb.Cell, active bool) {
		t.Helper()
		stored, err := runtime.GetCell(admin, key)
		require.NoError(t, err)
		require.Equal(t, want, stored)
		page, err := runtime.ListCells(admin, jobdb.ListCellsRequest{TenantID: tenant})
		require.NoError(t, err)
		if active {
			require.Equal(t, []jobdb.Cell{want}, page.Cells)
		} else {
			require.Empty(t, page.Cells)
		}
		job, err := runtime.GetJob(admin, history.JobKey)
		require.NoError(t, err)
		require.Equal(t, key.CellID, job.CellID)
		chapter, err := runtime.GetChapter(admin, jobdb.ChapterRef{JobKey: history.JobKey, Ordinal: 1})
		require.NoError(t, err)
		require.IsType(t, jobdb.JobAttemptOutcomeChapter{}, chapter.Body)
	}
	unarchive := func(ctx context.Context, key jobdb.CellKey, ifMatch string) (jobdb.Cell, error) {
		return cellregistry.UnarchiveCell(ctx, runtime, key, ifMatch, confirmedRestore)
	}
	archive := func(ctx context.Context, key jobdb.CellKey, ifMatch string) (jobdb.Cell, error) {
		return cellregistry.ArchiveCell(ctx, runtime, key, ifMatch)
	}
	assertRejected := func(current jobdb.Cell, active bool, grant string, change func(context.Context, jobdb.CellKey, string) (jobdb.Cell, error)) {
		t.Helper()
		_, err := change(admin, key, `"stale"`)
		require.ErrorIs(t, err, jobdb.ErrCellPrecondition)
		assertState(current, active)
		for _, denied := range []string{"cells.get", grant} {
			restricted, _ := jobdb.CellAuthorityFromContext(admin)
			delete(restricted.Permissions, denied)
			_, err := change(jobdb.WithCellAuthority(ctx, restricted), key, strconv.Quote(current.Revision))
			require.ErrorIs(t, err, jobdb.ErrCellForbidden)
			assertState(current, active)
		}
	}
	assertRejected(created, true, "cells.retire", archive)
	retired, err := archive(admin, key, strconv.Quote(created.Revision))
	require.NoError(t, err)
	require.Equal(t, created.ID, retired.ID)
	require.Equal(t, jobdb.CellRetired, retired.State)
	assertState(retired, false)
	assertRejected(retired, false, "cells.restore", unarchive)
	restored, err := unarchive(admin, key, strconv.Quote(retired.Revision))
	require.NoError(t, err)
	require.Equal(t, created.ID, restored.ID)
	require.Equal(t, jobdb.CellActive, restored.State)
	assertState(restored, true)
}

func TestUnarchiveRestoreSupportGate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		http.NotFound(w, req)
	}))
	defer server.Close()
	client, err := remote.New(server.URL, server.Client())
	require.NoError(t, err)
	key := jobdb.CellKey{TenantID: "tenant", CellID: "immutable-id"}
	_, err = cellregistry.UnarchiveCell(t.Context(), client, key, `"confirmed-revision"`, cellregistry.UnarchiveOptions{})
	require.ErrorIs(t, err, cellregistry.ErrRestoreSupportUnconfirmed)
	require.Zero(t, requests.Load())
	_, err = cellregistry.UnarchiveCell(t.Context(), nil, key, `"confirmed-revision"`, confirmedRestore)
	require.ErrorIs(t, err, jobdb.ErrCellRestoreUnsupported)
}

type restoreFunc func(context.Context, jobdb.RestoreCellRequest) (jobdb.Cell, error)

func (restore restoreFunc) RestoreCell(ctx context.Context, req jobdb.RestoreCellRequest) (jobdb.Cell, error) {
	return restore(ctx, req)
}

func TestUnarchivePreservesExactRequestAndFailureCause(t *testing.T) {
	key := jobdb.CellKey{TenantID: "tenant", CellID: "immutable-id"}
	const ifMatch = `"caller-confirmed-revision"`
	for _, cause := range []error{jobdb.ErrCellNotFound, jobdb.ErrCellRestoreUnsupported, errors.New("transport closed before response")} {
		t.Run(cause.Error(), func(t *testing.T) {
			calls := 0
			restore := restoreFunc(func(ctx context.Context, req jobdb.RestoreCellRequest) (jobdb.Cell, error) {
				calls++
				require.Equal(t, jobdb.RestoreCellRequest{CellKey: key, IfMatch: ifMatch}, req)
				return jobdb.Cell{}, cause
			})
			_, err := cellregistry.UnarchiveCell(t.Context(), restore, key, ifMatch, confirmedRestore)
			require.Equal(t, 1, calls)
			require.ErrorIs(t, err, cause)
			if _, _, structured := jobdb.CellErrorStatus(cause); structured {
				require.Same(t, cause, err)
			} else {
				require.ErrorIs(t, err, cellregistry.ErrRestoreOutcomeUnconfirmed)
				require.ErrorContains(t, err, "verify deployment support and cell state")
			}
		})
	}
}
