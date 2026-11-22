package jobdbschema

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/internal/executiontest"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type withdrawingSchemaRegistry struct {
	*captureRegistry
	withdraw func()
}

func (r withdrawingSchemaRegistry) RegisterJobSchema(ctx context.Context, req jobdb.RegisterJobSchemaRequest) (jobdb.JobSchemaInfo, error) {
	info, err := r.captureRegistry.RegisterJobSchema(ctx, req)
	if err == nil {
		r.withdraw()
	}
	return info, err
}

func TestExecutionBaseCatalogSchemaRegistrationRechecksPublication(t *testing.T) {
	for _, kind := range []string{"submit", "restart", "append"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now().UTC()
			base := "macos-15-arm64"
			catalog := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "tenant"}, Complete: true, Offerings: executiontest.Offerings(base), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
			reads := 0
			policy := executioncatalog.Policy{Required: true, Scope: catalog.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
				reads++
				raw, err := json.Marshal(catalog)
				require.NoError(t, err)
				return jobdb.ConfigurationSnapshot{Revision: catalog.Generation, Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
			}}
			registry := withdrawingSchemaRegistry{captureRegistry: &captureRegistry{}, withdraw: func() {
				catalog.Offerings = executiontest.Offerings()
				catalog.Generation = "withdrawn"
			}}
			ctx := executioncatalog.WithPolicy(context.Background(), policy)
			demand := execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "resolved", Effective: execution.Requirements{Base: &base}}
			payload, err := execution.PayloadWithDemand(nil, demand)
			require.NoError(t, err)
			job := jobdb.SubmitJob{TenantId: "tenant", JobType: "recipe", ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: payload}}
			switch kind {
			case "submit":
				submit := &missingSchemaSubmitter{}
				_, err = (Submitter{JobSubmitter: submit, Registry: registry}).SubmitJob(ctx, job)
				require.Equal(t, 1, submit.calls, "withdrawal must prevent the second publication attempt")
			case "restart":
				submit := &missingSchemaRestartSubmitter{}
				_, err = (Submitter{RestartSubmitter: submit, Registry: registry}).SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: jobdb.JobKey{TenantId: "tenant", JobId: "prior"}, ClientPayloadUpdate: job.ClientPayloadUpdate}})
				require.Equal(t, 1, submit.calls, "withdrawal must prevent the second publication attempt")
			case "append":
				submit := &appendCapture{failure: jobdb.ErrJobSchemaNotFound}
				_, err = Append(ctx, submit, registry, jobdb.StartOrAppendJob{Job: job})
				require.Equal(t, 1, submit.calls, "withdrawal must prevent the second contribution")
			}
			require.ErrorContains(t, err, executioncatalog.Unsupported)
			require.Equal(t, 1, registry.calls)
			require.Equal(t, 2, reads)
		})
	}
}

type catalogCaptureEngine struct {
	jobworkflow.Engine
	calls int
}

func (e *catalogCaptureEngine) SubmitJob(context.Context, jobdb.SubmitJob) (jobdb.JobHandle, error) {
	e.calls++
	return jobdb.JobHandle{}, nil
}
func (e *catalogCaptureEngine) SubmitRestartJob(context.Context, jobdb.SubmitRestartJobRequest) (jobdb.JobHandle, error) {
	e.calls++
	return jobdb.JobHandle{}, nil
}
func (e *catalogCaptureEngine) StartOrAppendJob(context.Context, jobdb.StartOrAppendJob) (jobdb.JobHandle, error) {
	e.calls++
	return jobdb.JobHandle{}, nil
}

func TestExecutionBaseCatalogEnginePassThrough(t *testing.T) {
	now := time.Now().UTC()
	catalog := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "tenant"}, Complete: true, Offerings: executiontest.Offerings(), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	raw, err := json.Marshal(catalog)
	require.NoError(t, err)
	policy := executioncatalog.Policy{Required: true, Scope: catalog.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	base := "unoffered"
	payload, err := execution.PayloadWithDemand(nil, execution.Demand{SchemaVersion: execution.DemandSchemaVersion, RecipeResolution: "resolved", Effective: execution.Requirements{Base: &base}})
	require.NoError(t, err)
	job := jobdb.SubmitJob{TenantId: "tenant", JobType: "custom", ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", Value: payload}}
	capture := &catalogCaptureEngine{}
	engine := WorkflowEngine{Engine: capture}
	for _, call := range []func(context.Context) (jobdb.JobHandle, error){
		func(ctx context.Context) (jobdb.JobHandle, error) { return engine.SubmitJob(ctx, job) },
		func(ctx context.Context) (jobdb.JobHandle, error) {
			return engine.StartOrAppendJob(ctx, jobdb.StartOrAppendJob{Job: job})
		},
		func(ctx context.Context) (jobdb.JobHandle, error) {
			return engine.SubmitRestartJob(ctx, jobdb.SubmitRestartJobRequest{Job: jobdb.SubmitRestartJob{PriorJobKey: jobdb.JobKey{TenantId: "tenant", JobId: "prior"}, ClientPayloadUpdate: job.ClientPayloadUpdate}})
		},
	} {
		_, err = call(executioncatalog.WithPolicy(context.Background(), policy))
		require.ErrorContains(t, err, executioncatalog.Unsupported)
	}
	require.Zero(t, capture.calls, "engine pass-through must not bypass required admission")
	_, err = engine.SubmitJob(context.Background(), job)
	require.NoError(t, err)
	require.Equal(t, 1, capture.calls, "unconfigured callers retain pass-through behavior")
}
