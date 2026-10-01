package childbroker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/stretchr/testify/require"
)

func TestLocalSubmitUsesBoundInterfaceAndDynamicPort(t *testing.T) {
	for _, bind := range []string{"127.0.0.1:0", "127.0.0.2:0", "0.0.0.0:0"} {
		t.Run(bind, func(t *testing.T) {
			listener, err := net.Listen("tcp4", bind)
			if err != nil && bind == "127.0.0.2:0" && runtime.GOOS != "linux" {
				t.Skipf("alternate loopback address unavailable on %s: %v", runtime.GOOS, err)
			}
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			submitter := &captureSubmitter{}
			broker, err := start(ctx, Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "parent", InvocationHash: "current-attempt"}, Submitter: submitter, ContainerReachable: true}, func(bool) (net.Listener, string, error) { return listener, "container-host.invalid", nil })
			require.NoError(t, err)
			defer broker.Close()
			require.Equal(t, "container-host.invalid", broker.Host())
			require.Contains(t, broker.localEndpoint, fmt.Sprintf(":%d/", broker.Port()))
			if bind == "127.0.0.2:0" {
				// Reproduce the old fixture rewrite: this port exists on a different
				// address, so substituting 127.0.0.1 must fail.
				conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(broker.Port())), time.Second)
				if conn != nil {
					conn.Close()
				}
				require.Error(t, dialErr)
			}
			req, err := NewSubmitRequest(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "child", RecipeName: "child"}, nil)
			require.NoError(t, err)
			localCtx := WithLocalSubmitter(ctx, broker)
			result, err := SubmitLocal(localCtx, req)
			require.NoError(t, err)
			require.Equal(t, "child", result.JobID)
			require.Equal(t, 1, submitter.calls)
			require.Equal(t, []string{"child"}, broker.StartedJobs().JobIDs)
			require.NoError(t, broker.Close())
			_, err = SubmitLocal(localCtx, req)
			require.Error(t, err, "closed invocation must not fall back to another broker")
		})
	}
}

type rejectBrokerTransport struct{}

func (rejectBrokerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("ambient transport must not receive local broker capability")
}

func TestLocalSubmitDoesNotUseAmbientProxyOrAdvertisedHost(t *testing.T) {
	original := http.DefaultTransport
	http.DefaultTransport = rejectBrokerTransport{}
	t.Cleanup(func() { http.DefaultTransport = original })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	broker, err := Start(ctx, Options{Current: jobcontext.Current{TenantID: "tenant", JobID: "parent"}, Submitter: &captureSubmitter{}})
	require.NoError(t, err)
	defer broker.Close()
	req, err := NewSubmitRequest(ctx, workflowctl.StartJob{TenantId: "tenant", JobID: "child", RecipeName: "child"}, nil)
	require.NoError(t, err)
	_, err = SubmitLocal(WithLocalSubmitter(ctx, broker), req)
	require.NoError(t, err)
	_, err = SubmitLocal(ctx, req)
	require.ErrorContains(t, err, "not available for this invocation")
}
