package childbroker

import (
	"context"
	"encoding/json"
	"github.com/colony-2/c2j/internal/executiontest"
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/colony-2/c2j/pkg/executioncatalog"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestExecutionBaseCatalogOrdinaryAndSelectedBroker(t *testing.T) {
	t.Setenv(executioncatalog.RequireEnv, "false") // an operation cannot lower parent policy
	now := time.Now().UTC()
	c := execution.BaseCatalog{Version: 1, Scope: execution.CatalogScope{Runtime: "https://test", Tenant: "tenant"}, Complete: true, Offerings: executiontest.Offerings("linux"), Generation: "one", PublishedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	raw, _ := json.Marshal(c)
	p := executioncatalog.Policy{Required: true, Scope: c.Scope, Read: func(context.Context) (jobdb.ConfigurationSnapshot, error) {
		return jobdb.ConfigurationSnapshot{Values: map[string]json.RawMessage{execution.BaseCatalogKey: raw}}, nil
	}}
	capture := &captureSubmitter{}
	current := jobcontext.Current{TenantID: "tenant", JobID: "parent", InvocationHash: "invocation"}
	s := &Server{executionContext: executioncatalog.WithPolicy(context.Background(), p), current: current, submitter: capture}
	base := "linux"
	initial, err := execution.Initial(nil, false, execution.Requirements{Base: &base, Capabilities: []string{"special"}})
	require.NoError(t, err)
	start := workflowctl.StartJob{TenantId: "tenant", Cell: "app", CellRepo: "example.test/app", RecipeName: "build", Execution: &initial}
	_, err = s.submit(context.Background(), SubmitRequest{Start: start})
	require.ErrorContains(t, err, executioncatalog.UnsupportedRequirements)
	require.Empty(t, s.started)
	require.Empty(t, capture.lastKey.JobId)
	start.JobID = "selected"
	start.GoalID = "goal"
	var d starter.Descriptor
	_, err = s.submit(context.Background(), SubmitRequest{Start: start, prepare: &d})
	require.NoError(t, err, "descriptor preparation remains pure")
	_, err = s.submit(context.Background(), SubmitRequest{Descriptor: &d})
	require.ErrorContains(t, err, executioncatalog.UnsupportedRequirements)
	require.Empty(t, s.started)
	require.Empty(t, capture.lastKey.JobId)
}
