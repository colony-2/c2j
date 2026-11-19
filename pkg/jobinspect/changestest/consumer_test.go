package changestest_test

import (
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/jobinspect/changestest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

func TestExternalConsumerL12(t *testing.T) {
	for _, backend := range []string{"sqlite", "http"} {
		t.Run(backend, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			db, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "consumer.db")})
			require.NoError(t, e)
			defer db.Close(ctx)
			var rt jobdb.ConsumerRuntime = db
			if backend == "http" {
				server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(db)))
				defer server.Close()
				rt, e = remote.New(server.URL, server.Client())
				require.NoError(t, e)
			}
			celltest.Create(t, rt, "tenant", "changes", "example.test/changes")
			f, e := changestest.Build(ctx, rt, changestest.Options{Directory: t.TempDir(), Revert: true})
			require.NoError(t, e)
			oracle, e := f.Oracle(ctx, f.Base, f.Head)
			require.NoError(t, e)
			require.NoError(t, f.DeleteCheckout())
			before, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			guard := &changestest.ReadOnlyRuntime{ConsumerRuntime: rt}
			api, e := jobinspect.NewChangesService(&jobinspect.Service{Runtime: guard, Tenant: "tenant"}, jobinspect.ChangesConfig{})
			require.NoError(t, e)
			page, e := api.History(ctx, jobinspect.ChangesRequest{JobID: f.Job.JobId, Limit: 1})
			require.NoError(t, e)
			require.NotEmpty(t, page.NextCursor)
			meta, body, e := api.OpenDiff(ctx, jobinspect.DiffRequest{ChangesRequest: jobinspect.ChangesRequest{JobID: f.Job.JobId, Selection: page.Selection}, From: f.Commits[0], Through: f.Head})
			require.NoError(t, e)
			actual, e := io.ReadAll(body)
			require.NoError(t, e)
			require.NoError(t, body.Close())
			require.True(t, meta.Complete)
			require.Equal(t, oracle, actual)
			require.Zero(t, guard.Mutations.Load())
			after, e := changestest.Inventory(ctx, rt, f.Job)
			require.NoError(t, e)
			require.Equal(t, string(before), string(after))
		})
	}
}
