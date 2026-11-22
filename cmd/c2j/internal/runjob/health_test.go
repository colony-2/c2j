package runjob

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/stretchr/testify/require"
)

func TestHealthJSONAndProtocol(t *testing.T) {
	at := time.Date(2026, 11, 21, 20, 0, 0, 0, time.FixedZone("offset", 3600))
	_, h, err := startHealth(context.Background(), Options{TenantID: "tenant", JobID: "job"}, at)
	require.NoError(t, err)
	defer h.close(nil)
	h.now = func() time.Time { return at }
	probe := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		require.Equal(t, "application/json", w.Header().Get("Content-Type"))
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		return w
	}
	w := probe("GET", "/health")
	require.Equal(t, 200, w.Code)
	var response map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response, 8)
	require.Equal(t, "tenant", response["tenant_id"])
	require.Equal(t, "job", response["job_id"])
	require.Equal(t, at.UTC().Format(time.RFC3339), response["started_at"])
	require.Nil(t, response["lease_expires_at"])
	require.Nil(t, response["extension_op"])
	require.Equal(t, "framework", response["task_type"])
	at = at.Add(time.Second)
	w = probe("GET", "/health")
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.NotEqual(t, response["observed_at"], response["started_at"])
	require.Equal(t, response["current_task_started_at"], response["started_at"])
	for _, method := range []string{"POST", "PUT", "DELETE", "HEAD", "OPTIONS"} {
		w = probe(method, "/health")
		require.Equal(t, 405, w.Code)
		require.Equal(t, "GET", w.Header().Get("Allow"))
	}
	for _, path := range []string{"/", "/health/", "//health", "/health/../"} {
		require.Equal(t, 404, probe("GET", path).Code)
	}
	past := at.Add(-time.Minute)
	h.source = func() executionruntime.ActivitySnapshot {
		return executionruntime.ActivitySnapshot{TaskType: "framework", CurrentTaskStartedAt: at, LeaseExpiresAt: &past}
	}
	require.Equal(t, 200, probe("GET", "/health").Code)
}

func TestHealthListenerLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, h, err := startHealth(context.Background(), Options{HealthPort: 1234, healthListen: func(network, address string) (net.Listener, error) {
		require.Equal(t, "tcp4", network)
		require.Equal(t, "0.0.0.0:1234", address)
		return listener, nil
	}}, time.Now())
	require.NoError(t, err)
	client := &http.Client{Timeout: time.Second}
	url := "http://" + listener.Addr().String() + "/health"
	resp, err := client.Get(url)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	resp.Body.Close()
	require.NoError(t, h.close(nil))
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	_, err = client.Get(url)
	require.Error(t, err)
}

func TestHealthUnexpectedServeFailure(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, h, err := startHealth(context.Background(), Options{HealthPort: 1234, healthListen: func(string, string) (net.Listener, error) { return listener, nil }}, time.Now())
	require.NoError(t, err)
	require.NoError(t, listener.Close())
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("serve failure did not cancel execution")
	}
	sentinel := jobdb.ErrExecutionLeaseLost
	err = h.close(sentinel)
	require.ErrorIs(t, err, sentinel)
	require.Contains(t, err.Error(), "serve run health")
}

func TestHealthDisabledDoesNotListen(t *testing.T) {
	_, h, err := startHealth(context.Background(), Options{healthListen: func(string, string) (net.Listener, error) {
		t.Fatal("disabled health attempted to bind")
		return nil, nil
	}}, time.Now())
	require.NoError(t, err)
	require.NoError(t, h.close(nil))
}

func TestHealthDisconnectedClient(t *testing.T) {
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	require.True(t, listener.Addr().(*net.TCPAddr).IP.IsUnspecified())
	_, h, err := startHealth(context.Background(), Options{HealthPort: 1234, healthListen: func(string, string) (net.Listener, error) { return listener, nil }}, time.Now())
	require.NoError(t, err)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	conn, err := net.DialTimeout("tcp4", address, time.Second)
	require.NoError(t, err)
	_, err = conn.Write([]byte("GET /health HTTP/1.1\r\nHost: worker\r\n\r\n"))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	resp, err := (&http.Client{Timeout: time.Second}).Get("http://" + address + "/health")
	require.NoError(t, err)
	resp.Body.Close()
	require.NoError(t, h.close(nil))
}
