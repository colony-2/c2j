package childbroker

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestBrokerCellCreationBindsTenantAndChecksAuthority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	access := permissions.NewAccess(ctx, permissions.JobDB{Cells: permissions.CellPermissions{Create: true, CellNames: []string{"reports"}}}, nil, nil,
		func(_ context.Context, req jobdb.CreateCellRequest) (jobdb.Cell, error) {
			calls++
			require.Equal(t, "tenant", req.TenantID)
			require.Equal(t, "retry-key", req.RequestID)
			return jobdb.Cell{ID: "created", TenantID: req.TenantID, Name: req.Name, Repo: req.Repo, Description: req.Description}, nil
		})
	server, err := Start(permissions.WithAccess(ctx, access), Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "parent"}, Submitter: &captureSubmitter{}})
	require.NoError(t, err)
	defer server.Close()
	broker := brokerConfig(t, server)
	req := jobdb.CreateCellRequest{TenantID: "forged", Name: "reports", Repo: "example.com/reports", Description: "Reports", RequestID: "retry-key"}
	cell, err := CreateCell(ctx, broker, req)
	require.NoError(t, err)
	require.Equal(t, "tenant", cell.TenantID)
	require.Equal(t, "Reports", cell.Description)
	req.Name = "denied"
	_, err = CreateCell(ctx, broker, req)
	require.ErrorIs(t, err, jobdb.ErrLeasePermissionDenied)
	for _, body := range []string{`{"tenantId":"forged","name":"reports","repo":"example.com/reports"}`, `{} {}`} {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, broker.Endpoint+"/cells", strings.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+broker.Token)
		request.Header.Set(sessionHeader, broker.SessionID)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		response.Body.Close()
	}
	broker.Token = "wrong"
	_, err = CreateCell(ctx, broker, req)
	require.Error(t, err)
	require.Equal(t, 1, calls)
}
