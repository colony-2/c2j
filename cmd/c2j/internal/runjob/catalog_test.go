package runjob

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExecutionBaseCatalogWorkerOptions(t *testing.T) {
	for _, v := range []string{"", "false", "true", "invalid"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv(executioncatalog.RequireEnv, v)
			opts := Options{JobID: "test", JobDBURI: "embed:///"}
			err := opts.Complete(context.Background())
			if v == "invalid" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.NoError(t, opts.Validate())
			}
		})
	}
}

func TestExecutionBaseCatalogRequiredDriverAcquiredAndSupplied(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		t.Run(fmt.Sprint(supplied), func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "must-not-run")
			f := newSuppliedCLI(t, "id: refusal\nsequence:\n - id: bad\n   op: command_execution\n   execution_needs: {base: unoffered}\n   inputs: {run: 'touch "+marker+"'}\n", time.Minute)
			target, err := defaults.ParseJobDBURI(f.opts.JobDBURI)
			require.NoError(t, err)
			now := time.Now().UTC()
			c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: target.RuntimeURL, Tenant: target.TenantID}, Complete: true, NamedBases: []string{"macos-15-arm64"}, Dynamic: []execution.DynamicBaseRule{}, Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
			raw, _ := json.Marshal(c)
			_, err = f.backend.SetTenantConfigurationValue(celltest.Context(context.Background()), jobdb.SetTenantConfigurationValueRequest{TenantID: target.TenantID, Name: execution.BaseCatalogKey, Value: raw})
			require.NoError(t, err)
			t.Setenv(executioncatalog.RequireEnv, "true")
			if supplied {
				err = f.run(t, true)
			} else {
				capability, e := remote.DecodeLeaseCapability(f.encoded)
				require.NoError(t, e)
				held, e := f.client.ImportLease(celltest.Context(context.Background()), capability)
				require.NoError(t, e)
				require.NoError(t, held.Reschedule(celltest.Context(context.Background()), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}))
				f.denyClaims.Store(false)
				err = Run(celltest.Context(context.Background()), f.opts)
			}
			require.ErrorContains(t, err, executioncatalog.Unsupported)
			_, err = os.Stat(marker)
			require.True(t, os.IsNotExist(err))
			info, err := f.backend.GetJob(celltest.Context(context.Background()), f.key)
			require.NoError(t, err)
			require.Empty(t, info.ClientPayload)
		})
	}
}
