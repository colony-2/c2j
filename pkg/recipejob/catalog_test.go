package recipejob

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/internal/executiontest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type catalogCapture struct{ calls int }

func (c *catalogCapture) SubmitJobRef(_ context.Context, j jobdb.SubmitJob) (jobref.Ref, error) {
	c.calls++
	return jobref.Ref{TenantID: j.TenantId, JobID: "accepted", Cell: j.Cell, CellID: "immutable", CellRepo: "example.test/app"}, nil
}
func TestExecutionBaseCatalogLibraryRequiredAndZeroPolicy(t *testing.T) {
	p := executioncatalog.Policy{Required: true}
	req := BuildStartJobRequest{BaseCatalog: &p, TenantID: "tenant", Target: ResolvedTarget{RepositorySource: "example.test/app", CellName: "app"}, Recipe: "build"}
	_, err := BuildStartJob(req)
	require.NoError(t, err, "preparation must not read live authority")
	capture := &catalogCapture{}
	_, err = SubmitRecipeJob(context.Background(), req, capture)
	require.Error(t, err)
	require.Zero(t, capture.calls)
	req.BaseCatalog = nil
	_, err = SubmitRecipeJob(context.Background(), req, capture)
	require.NoError(t, err)
	require.Equal(t, 1, capture.calls)
}

func TestExecutionBaseCatalogLibraryCompleteRequirements(t *testing.T) {
	now := time.Now().UTC()
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "tenant"}, Complete: true, Offerings: executiontest.Offerings("linux"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	c.Offerings[0].Resources.CPU = &execution.ResourceRule{State: "not_allowed"}
	c.Offerings[0].Resources.Memory = &execution.ResourceRule{State: "not_allowed"}
	p := executioncatalog.Policy{Required: true, Scope: c.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		raw, _ := json.Marshal(c)
		return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	base, cpu, memory := "linux", "1", "1Gi"
	for _, needs := range []execution.Requirements{{Base: &base, Capabilities: []string{"special"}}, {Base: &base, Resources: execution.Resources{CPU: &cpu}}, {Base: &base, Resources: execution.Resources{Memory: &memory}}} {
		req := BuildStartJobRequest{BaseCatalog: &p, TenantID: "tenant", Target: ResolvedTarget{RepositorySource: "example.test/app", CellName: "app"}, Recipe: "build", ExecutionRequirements: needs}
		_, err := BuildStartJob(req)
		require.NoError(t, err)
		capture := &catalogCapture{}
		_, err = SubmitRecipeJob(context.Background(), req, capture)
		require.ErrorContains(t, err, executioncatalog.UnsupportedRequirements)
		require.Zero(t, capture.calls)
	}
}
