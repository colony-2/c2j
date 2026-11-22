package compiler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/pkg/grouping"
	"github.com/colony-2/c2j/pkg/jobdbschema"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/colony-2/jobdb/pkg/jobdb/runtime/remote"
	wf "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

type groupingPinnedSubmitter struct {
	jobdbschema.WorkflowEngine
	schema string
}

func (s groupingPinnedSubmitter) StartOrAppendJobRef(ctx context.Context, q jobdb.StartOrAppendJob) (jobref.Ref, error) {
	q.Job.Schema = &jobdb.JobSchemaSelector{Hash: s.schema}
	return s.WorkflowEngine.StartOrAppendJobRef(ctx, q)
}
func TestGroupingCreatorPinnedSchema(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		t.Run(transport, func(t *testing.T) {
			f := newGroupingFixture(t, transport, groupingCountRecipe)
			var schema map[string]any
			require.NoError(t, json.Unmarshal(jobdbschema.Schema(), &schema))
			schema["appendShape"] = map[string]any{"type": "object", "required": []string{"content"}, "properties": map[string]any{"content": map[string]any{"type": "object", "required": []string{"id"}, "properties": map[string]any{"id": map[string]any{"const": 1}}}}}
			raw, err := json.Marshal(schema)
			require.NoError(t, err)
			registered, err := f.eng.Registry.RegisterJobSchema(f.ctx, jobdb.RegisterJobSchemaRequest{TenantId: "tenant", Schema: raw})
			require.NoError(t, err)
			a, err := starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "pinned", "content": map[string]any{"id": 1}}}, groupingPinnedSubmitter{f.eng, registered.SchemaHash}, f.rec)
			require.NoError(t, err)
			_, err = starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "pinned", "content": map[string]any{"id": 2}}}, f.eng, f.rec)
			require.Error(t, err)
			info, err := f.rt.GetJob(f.ctx, a.Key())
			require.NoError(t, err)
			require.Equal(t, registered.SchemaHash, info.SchemaHash)
			require.Equal(t, []any{float64(1)}, f.result(t, a)["ids"])
		})
	}
}

type groupingLostResponseTransport struct {
	inner    http.RoundTripper
	requests atomic.Int32
}

func (r *groupingLostResponseTransport) RoundTrip(q *http.Request) (*http.Response, error) {
	resp, err := r.inner.RoundTrip(q)
	if strings.HasSuffix(q.URL.Path, "/jobs/start-or-append") {
		r.requests.Add(1)
		if err == nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("response lost after durable acceptance")
		}
	}
	return resp, err
}
func TestGroupingTransportFailureDoesNotResubmit(t *testing.T) {
	f := newGroupingFixture(t, "sqlite", groupingCountRecipe)
	s := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(f.rt)))
	defer s.Close()
	transport := &groupingLostResponseTransport{inner: s.Client().Transport}
	client, err := remote.New(s.URL, &http.Client{Transport: transport})
	require.NoError(t, err)
	engine, err := wf.NewEngineBuilder().WithRuntime(client).BuildEngine()
	require.NoError(t, err)
	eng := jobdbschema.WorkflowEngine{Engine: engine, Registry: client}
	_, err = starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "ambiguous", "content": map[string]any{"id": 1}}}, eng, f.rec)
	require.ErrorContains(t, err, "response lost after durable acceptance")
	require.EqualValues(t, 1, transport.requests.Load())
	jobs, err := f.rt.ListJobs(f.ctx, jobdb.ListJobsRequest{TenantIds: []string{"tenant"}, AppendKeys: []string{"ambiguous"}})
	require.NoError(t, err)
	require.Len(t, jobs.Jobs, 1)
	info := jobs.Jobs[0]
	out := f.run(t, info.JobKey, nil)
	require.Equal(t, wf.JobRunCompleted, out.Status, "%v", out.JobError)
	var got map[string]any
	require.NoError(t, json.Unmarshal(out.Output.GetDataOrPanic(), &got))
	require.EqualValues(t, 1, got["count"])
}

func TestGroupingCreatorInputsRetainSchemaValidation(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		for _, input := range []map[string]any{nil, {"config": false}, {"config": "ok", "unexpected": "value"}} {
			t.Run(fmt.Sprintf("%s/%v", transport, input), func(t *testing.T) {
				f := newGroupingFixture(t, transport, `id: batch
input_schema:
  config: {type: string, required: true}
sequence: []
outputs: {config: '${{ inputs.config }}'}
`)
				f.start.Inputs = input
				ref := f.submit(t, "schema", 1, 0)
				out := f.run(t, ref.Key(), nil)
				require.Equal(t, wf.JobRunFailed, out.Status)
				require.Error(t, out.JobError)
				require.Contains(t, out.JobError.Error(), "field")
			})
		}
	}
}

// Inject at the backend boundary so the real starter, schema wrapper and HTTP
// codec must surface the failure without using ordinary submission as a fallback.
type groupingRejectedRuntime struct {
	jobdb.ConsumerRuntime
	jobdb.ConfigurationAdmin
	err             error
	appends, starts int
}

func (r *groupingRejectedRuntime) StartOrAppendJob(context.Context, jobdb.StartOrAppendJobRequest) (jobdb.JobHandle, error) {
	r.appends++
	return jobdb.JobHandle{}, r.err
}

func (r *groupingRejectedRuntime) SubmitJob(ctx context.Context, req jobdb.SubmitJobRequest) (jobdb.JobHandle, error) {
	r.starts++
	return r.ConsumerRuntime.SubmitJob(ctx, req)
}

func TestGroupingBackendRejectionsNeverFallBack(t *testing.T) {
	for _, transport := range []string{"sqlite", "http"} {
		for _, failure := range []error{jobdb.ErrAppendBusy, jobdb.ErrLeasePermissionDenied, jobdb.ErrChapterApplicationPayloadTooLarge, fmt.Errorf("grouping operation unsupported")} {
			t.Run(transport+"/"+failure.Error(), func(t *testing.T) {
				f := newGroupingFixture(t, "sqlite", groupingCountRecipe)
				fault := &groupingRejectedRuntime{ConsumerRuntime: f.rt, ConfigurationAdmin: f.rt.(jobdb.ConfigurationAdmin), err: failure}
				var rt jobdb.ConsumerRuntime = fault
				if transport == "http" {
					s := httptest.NewServer(celltest.Handler(remote.NewConsumerServer(fault)))
					t.Cleanup(s.Close)
					var err error
					rt, err = remote.New(s.URL, s.Client())
					require.NoError(t, err)
				}
				eng, err := wf.NewEngineBuilder().WithRuntime(rt).BuildEngine()
				require.NoError(t, err)
				wrapped := jobdbschema.WorkflowEngine{Engine: eng, Registry: f.eng.Registry}
				_, err = starter.StartOrAppendRecipeJob(f.ctx, f.start, grouping.Spec{Present: true, Value: map[string]any{"key": "rejected", "content": map[string]any{}}}, wrapped, f.rec)
				require.Error(t, err)
				if transport == "sqlite" || errors.Is(failure, jobdb.ErrAppendBusy) || errors.Is(failure, jobdb.ErrLeasePermissionDenied) || errors.Is(failure, jobdb.ErrChapterApplicationPayloadTooLarge) {
					require.ErrorIs(t, err, failure)
					require.False(t, jobdb.IsExecutionInfrastructureError(err), "known semantic rejections must keep their classification")
				} else {
					// Arbitrary backend messages have no safe wire contract. The
					// client reports an uncertain runtime failure without exposing them.
					require.True(t, jobdb.IsExecutionInfrastructureError(err))
					require.NotContains(t, err.Error(), failure.Error())
				}
				require.Equal(t, 1, fault.appends)
				require.Zero(t, fault.starts)
			})
		}
	}
}
