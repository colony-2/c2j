package starter

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestExecutionBaseCatalogPublicationAndPurePreparation(t *testing.T) {
	now := time.Now().UTC()
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "tenant"}, Complete: true, NamedBases: []string{"macos-15-arm64"}, Dynamic: []execution.DynamicBaseRule{}, Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	reads := 0
	p := executioncatalog.Policy{Required: true, Scope: c.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		reads++
		raw, _ := json.Marshal(c)
		return jobdb.ConfigurationSnapshot{Revision: "one", Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	base := "macos-15-arm64"
	initial, err := execution.Initial(nil, false, execution.Requirements{Base: &base})
	require.NoError(t, err)
	start := workflowctl.StartJob{TenantId: "tenant", Cell: "app", CellRepo: "example.test/app", RecipeName: "build", Execution: &initial}
	ctx := executioncatalog.WithPolicy(context.Background(), p)
	job, err := PrepareRecipeJob(ctx, start, StartRecipeJobOptions{BaseCatalog: &p})
	require.NoError(t, err)
	d, err := NewDescriptor(ctx, job)
	require.NoError(t, err)
	require.Zero(t, reads)
	c.NamedBases = []string{}
	capture := &captureSubmitter{}
	_, err = d.Submit(ctx, capture)
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	require.Nil(t, capture.job.Data)
	_, err = StartRecipeJobWithOptions(context.Background(), start, capture, StartRecipeJobOptions{BaseCatalog: &p})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	require.Nil(t, capture.job.Data)
	c.NamedBases = []string{base}
	_, err = d.Submit(ctx, capture)
	require.NoError(t, err)
	require.Equal(t, job.Metadata, capture.job.Metadata)
	p.Read = nil
	_, err = StartRecipeJobWithOptions(context.Background(), start, capture, StartRecipeJobOptions{BaseCatalog: &p})
	require.ErrorContains(t, err, executioncatalog.Unavailable)
	_, err = d.Submit(context.Background(), capture)
	require.NoError(t, err, "zero policy stays compatible")
}
func TestExecutionBaseCatalogRestartInitialDemand(t *testing.T) {
	base := "unoffered"
	initial, err := execution.Initial(nil, true, execution.Requirements{Base: &base})
	require.NoError(t, err)
	metadata, _ := json.Marshal(JobMetadata{Execution: &initial})
	engine := &captureEngine{info: jobdb.JobSummary{Cell: "app", Metadata: metadata}}
	now := time.Now().UTC()
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "tenant"}, Complete: true, NamedBases: []string{}, Dynamic: []execution.DynamicBaseRule{}, Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	raw, _ := json.Marshal(c)
	p := executioncatalog.Policy{Required: true, Scope: c.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	_, err = RestartRecipeJob(executioncatalog.WithPolicy(context.Background(), p), engine, jobdb.JobKey{TenantId: "tenant", JobId: "source"}, SubmissionRestartScopeID, nil, RestartOptions{RequestID: "one"})
	require.ErrorContains(t, err, executioncatalog.Unsupported)
	require.Nil(t, engine.last)
	engine.info.Metadata = nil
	_, err = RestartRecipeJob(executioncatalog.WithPolicy(context.Background(), p), engine, jobdb.JobKey{TenantId: "tenant", JobId: "source"}, SubmissionRestartScopeID, nil, RestartOptions{RequestID: "two"})
	require.NoError(t, err)
	require.NotNil(t, engine.last.ClientPayloadUpdate)
}
