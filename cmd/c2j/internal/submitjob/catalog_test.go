package submitjob

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/executiontest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutionBaseCatalogStandaloneSubmission(t *testing.T) {
	for _, env := range os.Environ() {
		name, _, _ := strings.Cut(env, "=")
		if strings.HasPrefix(name, "C2J_CURRENT_") || strings.HasPrefix(name, "C2J_CHILD_JOB_") || strings.HasPrefix(name, "C2J_WORKSPACE_") {
			t.Setenv(name, "")
		}
	}
	t.Setenv(executioncatalog.RequireEnv, "true")
	ctx := celltest.Context(context.Background())
	rt := toy.New()
	celltest.Create(t, rt, "t", "app", "example.test/app")
	server := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(rt)))
	defer server.Close()
	now := time.Now().UTC()
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: server.URL, Tenant: "t"}, Complete: true, Offerings: executiontest.Offerings("macos-15-arm64"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	raw, _ := json.Marshal(c)
	_, err := rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "t", Name: execution.BaseCatalogKey, Value: raw})
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "recipe.yaml")
	require.NoError(t, os.WriteFile(file, []byte("id: probe\ninput_schema: {prompt: {type: string}}\nop: command_execution\ninputs: {run: 'true'}\n"), 0600))
	base := "bad"
	var out bytes.Buffer
	err = Run(ctx, Options{JobDBURI: server.URL + "/t", RecipeFile: file, Cell: "app", CellRepo: "example.test/app", Prompt: "probe", ExecutionRequirements: execution.Requirements{Base: &base}, Stdout: &out, Stderr: &out})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	jobs, err := rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}})
	require.NoError(t, err)
	require.Empty(t, jobs.Jobs)

	for _, needs := range []execution.Requirements{
		{Capabilities: []string{"special"}},
		{Resources: execution.Resources{CPU: catalogString("16")}},
		{Resources: execution.Resources{Memory: catalogString("64Gi")}},
	} {
		c.Offerings[0].Resources.CPU = &execution.ResourceRule{State: "range", Max: catalogString("8")}
		c.Offerings[0].Resources.Memory = &execution.ResourceRule{State: "range", Max: catalogString("32Gi")}
		raw, _ = json.Marshal(c)
		_, err = rt.SetTenantConfigurationValue(ctx, jobdb.SetTenantConfigurationValueRequest{TenantID: "t", Name: execution.BaseCatalogKey, Value: raw})
		require.NoError(t, err)
		base = "macos-15-arm64"
		needs.Base = &base
		err = Run(ctx, Options{JobDBURI: server.URL + "/t", RecipeFile: file, Cell: "app", CellRepo: "example.test/app", Prompt: "probe", ExecutionRequirements: needs, Stdout: &out, Stderr: &out})
		require.ErrorContains(t, err, executioncatalog.UnsupportedRequirements)
		jobs, err = rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}})
		require.NoError(t, err)
		require.Empty(t, jobs.Jobs)
	}
	base = "macos-15-arm64"
	err = Run(ctx, Options{JobDBURI: server.URL + "/t", RecipeFile: file, Cell: "app", CellRepo: "example.test/app", Prompt: "probe", ExecutionRequirements: execution.Requirements{Base: &base}, Stdout: &out, Stderr: &out})
	require.NoError(t, err)
	jobs, err = rt.ListJobs(ctx, jobdb.ListJobsRequest{TenantIds: []string{"t"}})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 1)
}

func catalogString(s string) *string { return &s }
