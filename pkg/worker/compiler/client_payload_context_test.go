package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
	"github.com/stretchr/testify/require"
)

// Test contexts without a runtime must not pretend to have a live lease or to
// have published an update. Real payload handoff is covered with runtime tests.
type noLeaseClientContext struct{}

func (noLeaseClientContext) ClientPayload() json.RawMessage { return nil }
func (noLeaseClientContext) ClientPayloadRevision() int64   { return 0 }
func (noLeaseClientContext) Yield(context.Context, jobdb.RescheduleExecutionRequest) error {
	return fmt.Errorf("yield requires an execution lease")
}

type payloadJobContext struct {
	stubJobContext
	payload  json.RawMessage
	revision int64
	yields   []jobdb.RescheduleExecutionRequest
	yieldErr error
}

func (c *payloadJobContext) ClientPayload() json.RawMessage {
	return append(json.RawMessage(nil), c.payload...)
}
func (c *payloadJobContext) ClientPayloadRevision() int64 { return c.revision }
func (c *payloadJobContext) Yield(_ context.Context, req jobdb.RescheduleExecutionRequest) error {
	c.yields = append(c.yields, req)
	return c.yieldErr
}

func TestClientPayloadContextForwarding(t *testing.T) {
	for _, tt := range []struct {
		name string
		wrap func(jobworkflow.JobContext) jobworkflow.JobContext
	}{
		{"thinpack", func(c jobworkflow.JobContext) jobworkflow.JobContext { return newThinPackForwardingJobContext(c) }},
		{"timeout", func(c jobworkflow.JobContext) jobworkflow.JobContext {
			return withExecutionTimeout(c, time.Minute, "test")
		}},
		{"nested", func(c jobworkflow.JobContext) jobworkflow.JobContext {
			return newThinPackForwardingJobContext(withExecutionTimeout(c, time.Minute, "test"))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inner := &payloadJobContext{payload: json.RawMessage(`{"cursor":"one"}`), revision: 7}
			wrapped := tt.wrap(inner)
			require.JSONEq(t, string(inner.payload), string(wrapped.ClientPayload()))
			require.EqualValues(t, 7, wrapped.ClientPayloadRevision())
			revision := int64(7)
			req := jobdb.RescheduleExecutionRequest{
				NextRoute: jobdb.Route{JobType: "recipe"}, WaitForJobIDs: []string{"child"},
				ClientPayloadUpdate: &jobdb.ClientPayloadUpdate{Mode: "reset", ExpectedRevision: &revision, Value: json.RawMessage(`{"cursor":"two"}`)},
			}
			require.NoError(t, wrapped.Yield(context.Background(), req))
			require.Equal(t, []jobdb.RescheduleExecutionRequest{req}, inner.yields)
			inner.yieldErr = jobdb.ErrConflict
			require.ErrorIs(t, wrapped.Yield(context.Background(), req), jobdb.ErrConflict)
		})
	}
}

func TestExpiredTimeoutDoesNotYield(t *testing.T) {
	inner := &payloadJobContext{}
	wrapped := &timeoutJobContext{inner: inner, deadline: time.Now().Add(-time.Second)}
	require.ErrorIs(t, wrapped.Yield(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}), context.DeadlineExceeded)
	require.Empty(t, inner.yields)
}

func TestValidationDoesNotExposeOrPublishLiveClientPayload(t *testing.T) {
	inner := &payloadJobContext{payload: json.RawMessage(`{"live":true}`), revision: 3}
	for _, wrapped := range []*validationJobContext{{inner: inner}, {}} {
		require.Nil(t, wrapped.ClientPayload())
		require.Zero(t, wrapped.ClientPayloadRevision())
		require.ErrorContains(t, wrapped.Yield(context.Background(), jobdb.RescheduleExecutionRequest{NextRoute: jobdb.Route{JobType: "recipe"}}), "not supported during validation")
	}
	require.Empty(t, inner.yields)
}
