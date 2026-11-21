package childbroker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtensionWaitBrokerClosePreservesError(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)
	done := make(chan error, 1)
	go func() {
		resp, err := server.Client().Get(server.URL)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		done <- err
	}()
	<-started
	broker := &Server{server: server.Config}
	require.ErrorIs(t, broker.Close(), context.DeadlineExceeded)
	// Context cancellation and the invocation's checked cleanup can both close
	// the broker. The second caller must still observe the first failure.
	require.ErrorIs(t, broker.Close(), context.DeadlineExceeded)
	server.CloseClientConnections()
	<-done
}
