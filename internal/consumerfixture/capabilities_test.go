package consumerfixture_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/consumerfixture"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestC05RetainedServerCapabilities(t *testing.T) {
	for _, transport := range []string{"sqlite", "http", "http-options"} {
		t.Run(transport, func(t *testing.T) {
			ctx := jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(context.Background()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })
			db, err := sqlite.NewFromConfig(ctx, sqlite.Config{DBPath: filepath.Join(t.TempDir(), "jobs.db")})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close(ctx)) })
			retained := consumerfixture.New(db)
			require.Equal(t, db.ConfigurationLimits(), retained.ConfigurationLimits())
			require.Equal(t, db.SecretResolutionLimits(), retained.SecretResolutionLimits())
			var rt jobdb.ConsumerRuntime = retained
			if transport != "sqlite" {
				handler := remote.NewConsumerServer(retained)
				if transport == "http-options" {
					handler, err = remote.NewConsumerServerWithOptions(retained, remote.ServerOptions{LeaseTokenSigningKey: []byte(strings.Repeat("k", 32))})
					require.NoError(t, err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					handler.ServeHTTP(w, r.WithContext(jobdb.WithPrimaryGoalSelectionAuthorizer(celltest.Context(r.Context()), func(context.Context, jobdb.GoalKey, jobdb.ResolvedCell) error { return nil })))
				}))
				t.Cleanup(server.Close)
				rt, err = remote.New(server.URL, server.Client())
				require.NoError(t, err)
			}
			require.True(t, retained.SupportsPrimaryGoalSelection())
			celltest.Create(t, rt, "tenant", "test", "example.test/test")
			admin := rt.(jobdb.ConfigurationAdmin)
			revision, err := admin.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "tenant", Name: "setting", Value: json.RawMessage(`{"n":9007199254740993}`)})
			require.NoError(t, err)
			registry := rt.(jobdb.JobSchemaRegistry)
			schema, err := registry.RegisterJobSchema(ctx, jobdb.RegisterJobSchemaRequest{TenantId: "tenant", Schema: json.RawMessage(`{"chapterShape":{"type":"object"}}`)})
			require.NoError(t, err)
			stored, err := registry.GetJobSchema(ctx, jobdb.JobSchemaKey{TenantId: "tenant", SchemaHash: schema.SchemaHash})
			require.NoError(t, err)
			require.Equal(t, schema, stored)
			schemas, err := registry.ListJobSchemas(ctx, jobdb.ListJobSchemasRequest{TenantId: "tenant"})
			require.NoError(t, err)
			require.NotEmpty(t, schemas)
			owner, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "owner", JobType: "work", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			selected, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "selected", GoalID: owner.GoalID, JobType: "work", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic(nil)}})
			require.NoError(t, err)
			require.Equal(t, owner.GoalID, selected.GoalID)
			require.NoError(t, rt.ClearCrashConcern(ctx, jobdb.ClearCrashConcernRequest{JobKey: owner.JobKey, Reason: "reviewed"}))
			lease, err := rt.GetJobLease(ctx, jobdb.GetJobLeaseRequest{JobKey: owner.JobKey, WorkerID: "worker", Routes: []jobdb.Route{{JobType: "work"}}, LeaseDuration: time.Minute})
			require.NoError(t, err)
			require.NotNil(t, lease)
			require.NoError(t, lease.KeepAlive(ctx))
			renewed, err := lease.(jobdb.RenewableExecutionLease).Renew(ctx)
			require.NoError(t, err)
			require.Equal(t, lease.LeaseID(), renewed.LeaseID())
			config, err := renewed.GetConfiguration(ctx)
			require.NoError(t, err)
			require.Equal(t, revision, config.Revision)
			require.JSONEq(t, `{"n":9007199254740993}`, string(config.Values["setting"]))
			_, err = rt.GetConfiguration(ctx, jobdb.GetConfigurationRequest{JobKey: owner.JobKey, LeaseID: renewed.LeaseID()})
			require.ErrorIs(t, err, jobdb.ErrExecutionLeaseLost)
			child, err := renewed.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: jobdb.SubmitJob{TenantId: "tenant", JobID: "child", JobType: "work", Cell: "test", CellRepo: "example.test/test", Data: jobdb.NewTaskDataOrPanic("child")}})
			require.NoError(t, err)
			cell, err := renewed.LookupCell(ctx, "test")
			require.NoError(t, err)
			require.Equal(t, owner.CellID, cell.CellID)
			if transport != "sqlite" {
				delegator := rt.(jobdb.LeaseDelegator)
				delegated, err := delegator.CreateDelegation(ctx, renewed, jobdb.CreateDelegationRequest{Permissions: jobdb.DelegationPermissions{Read: &jobdb.DelegationChapters{Ordinals: []int64{0}}}})
				require.NoError(t, err)
				_, err = delegated.GetConfiguration(ctx)
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
				_, err = delegated.ResolveSecrets(ctx, []string{"forbidden"})
				require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
			}
			require.ErrorIs(t, rt.ClearCrashConcern(ctx, jobdb.ClearCrashConcernRequest{JobKey: owner.JobKey, Reason: "active"}), jobdb.ErrConflict)
			require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: owner.JobKey, Reason: "stop"}))
			require.ErrorIs(t, renewed.KeepAlive(ctx), jobdb.ErrExecutionLeaseLost)
			require.NoError(t, rt.CancelJob(ctx, jobdb.CancelJobRequest{JobKey: child.JobKey}))
			deleted, err := rt.(jobdb.JobDeletionRuntime).DeleteJobs(ctx, jobdb.DeleteJobsRequest{TenantID: "tenant", JobIDs: []string{child.JobKey.JobId}, DryRun: true})
			require.NoError(t, err)
			require.EqualValues(t, 1, deleted.MatchedCount)
			_, err = admin.RemoveTenantConfigurationValue(ctx, jobdb.RemoveTenantConfigurationValueRequest{TenantID: "tenant", Name: "setting", ExpectedRevision: &revision})
			require.NoError(t, err)
			_, err = registry.ArchiveJobSchema(ctx, jobdb.JobSchemaKey{TenantId: "tenant", SchemaHash: schema.SchemaHash})
			require.NoError(t, err)
		})
	}
}
