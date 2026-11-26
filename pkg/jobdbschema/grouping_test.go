package jobdbschema

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobdbcelltest "github.com/colony-2/jobdb/pkg/jobdb/celltest"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/toy"
	"github.com/stretchr/testify/require"
)

type appendCapture struct {
	calls   int
	failure error
}

func (s *appendCapture) StartOrAppendJob(context.Context, jobdb.StartOrAppendJob) (jobdb.JobHandle, error) {
	s.calls++
	if s.calls == 1 {
		return jobdb.JobHandle{}, s.failure
	}
	return jobdb.JobHandle{JobKey: jobdb.JobKey{TenantId: "tenant", JobId: "job"}, GoalID: "goal"}, nil
}
func TestGroupedSchemaRegistrationOnlyDefinitiveMiss(t *testing.T) {
	for _, failure := range []error{jobdb.ErrJobSchemaNotFound, errors.New("transport timeout mentioning job schema not found"), jobdb.ErrLeasePermissionDenied} {
		capture := &appendCapture{failure: failure}
		registry := &captureRegistry{}
		_, err := Append(context.Background(), capture, registry, jobdb.StartOrAppendJob{Job: jobdb.SubmitJob{TenantId: "tenant"}})
		if errors.Is(failure, jobdb.ErrJobSchemaNotFound) {
			require.NoError(t, err)
			require.Equal(t, 2, capture.calls)
			require.Equal(t, 1, registry.calls)
		} else {
			require.ErrorIs(t, err, failure)
			require.Equal(t, 1, capture.calls)
			require.Zero(t, registry.calls)
		}
	}
}
func TestGroupedAppendSchema(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"content":{},"artifacts":[]}`, true},
		{`{"artifacts":[{"kind":"external","name":"a","external":{"url":"https://example.test"}}]}`, true},
		{`null`, false}, {`[]`, false}, {`{"artifacts":[]}`, false}, {`{"content":null,"artifacts":[]}`, false},
		{`{"content":1,"artifacts":[]}`, false}, {`{"content":{},"artifacts":null}`, false}, {`{"content":{},"artifacts":[],"unknown":true}`, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			rt := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
			celltest.Create(t, rt, "tenant", "test", "example.test/test")
			require.NoError(t, Register(ctx, rt, "tenant"))
			req := jobdb.StartOrAppendJob{AppendKey: "key", Append: json.RawMessage(tc.raw), Job: jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(map[string]any{"tenantId": "tenant", "cell": "test", "recipe": "batch", "context": map[string]any{}})}}
			require.NoError(t, SetSubmitJobSchema(&req.Job))
			_, err := rt.StartOrAppendJob(ctx, jobdb.StartOrAppendJobRequest{Job: req})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestHookPolicySchemaAdmission(t *testing.T) {
	const start = `"tenantId":"tenant","cell":"test","recipe":"build"`
	const lineage = `"child_hook_lineage":{"repository":"example.test/test"}`
	const ceiling = `"version":1,"grant":{},"limit":{},"inputs":{"foreign":true}`
	for _, scenario := range []struct {
		name, payload string
		valid         bool
	}{
		{"legacy", `"context":{}`, true},
		{"legacy-child", `"context":{"inherited_permissions":{"version":1,"child_policy":{}}}`, true},
		{"execution", `"hook_policy_version":1,"context":{}`, true},
		{"execution-child", `"hook_policy_version":1,"context":{"inherited_permissions":{` + ceiling + `,` + lineage + `}}`, true},
		{"unknown-version", `"hook_policy_version":2,"context":{}`, false},
		{"null-version", `"hook_policy_version":null,"context":{}`, false},
		{"unmarked-lineage", `"context":{"inherited_permissions":{` + ceiling + `,` + lineage + `}}`, false},
		{"missing-ceiling", `"hook_policy_version":1,"context":{"inherited_permissions":{"version":1,` + lineage + `}}`, false},
		{"malformed-lineage", `"hook_policy_version":1,"context":{"inherited_permissions":{` + ceiling + `,"child_hook_lineage":{"repository":""}}}`, false},
		{"legacy-policy-with-marker", `"hook_policy_version":1,"context":{"inherited_permissions":{"version":1,"child_policy":{}}}`, false},
		{"conflicting-lineage", `"hook_policy_version":1,"context":{"inherited_permissions":{` + ceiling + `,` + lineage + `,"child_policy":{}}}`, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := celltest.Context(context.Background())
			runtime := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
			celltest.Create(t, runtime, "tenant", "test", "example.test/test")
			require.NoError(t, Register(ctx, runtime, "tenant"))
			job := jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(json.RawMessage(`{` + start + `,` + scenario.payload + `}`))}
			require.NoError(t, SetSubmitJobSchema(&job))
			_, submitErr := runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
			_, appendErr := runtime.StartOrAppendJob(ctx, jobdb.StartOrAppendJobRequest{Job: jobdb.StartOrAppendJob{Job: job, AppendKey: "batch", Append: json.RawMessage(`{"content":{},"artifacts":[]}`)}})
			if scenario.valid {
				require.NoError(t, submitErr)
				require.NoError(t, appendErr)
			} else {
				require.Error(t, submitErr)
				require.Error(t, appendErr)
			}
		})
	}
}

func TestGroupedSchemaKeepsOrdinaryStartClosed(t *testing.T) {
	ctx := celltest.Context(context.Background())
	rt := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
	celltest.Create(t, rt, "tenant", "test", "example.test/test")
	require.NoError(t, Register(ctx, rt, "tenant"))
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"tenantId":"tenant","cell":"test","recipe":"batch","context":{}}`, true},
		{`{"tenantId":"tenant","cell":"test","recipe":"batch","context":{},"unrecognized":1}`, false},
		{`{"start":{"recipe":"batch"},"appends":[{"content":{},"artifacts":[]}]}`, false},
	} {
		q := jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(json.RawMessage(tc.raw))}
		require.NoError(t, SetSubmitJobSchema(&q))
		_, err := rt.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: q})
		if tc.valid {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestRecipeStatusSchemaAdmission(t *testing.T) {
	ctx := celltest.Context(context.Background())
	runtime := toy.New(toy.WithProjects(jobdbcelltest.UnrestrictedProjects{}))
	celltest.Create(t, runtime, "tenant", "test", "example.test/test")
	require.NoError(t, Register(ctx, runtime, "tenant"))
	for _, scenario := range []struct {
		field string
		valid bool
	}{
		{"", true},
		{`,"status_history_version":1`, true},
		{`,"status_history_version":0`, false},
		{`,"status_history_version":2`, false},
		{`,"status_history_version":null`, false},
		{`,"status_history_version":"1"`, false},
	} {
		payload := json.RawMessage(`{"tenantId":"tenant","cell":"test","recipe":"build","context":{}` + scenario.field + `}`)
		job := jobdb.SubmitJob{TenantId: "tenant", Cell: "test", JobType: "recipe", Data: jobdb.NewTaskDataOrPanic(payload)}
		require.NoError(t, SetSubmitJobSchema(&job))
		_, err := runtime.SubmitJob(ctx, jobdb.SubmitJobRequest{Job: job})
		if scenario.valid {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, jobdb.ErrJobSchemaValidation)
		}
	}
}
