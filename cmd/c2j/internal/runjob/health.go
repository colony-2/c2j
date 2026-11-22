package runjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/colony-2/c2j/pkg/executionruntime"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type healthResponse struct {
	TenantID   string    `json:"tenant_id"`
	JobID      string    `json:"job_id"`
	StartedAt  time.Time `json:"started_at"`
	ObservedAt time.Time `json:"observed_at"`
	executionruntime.ActivitySnapshot
}

type healthHost struct {
	mu      sync.RWMutex
	source  func() executionruntime.ActivitySnapshot
	initial healthResponse
	now     func() time.Time
	server  *http.Server
	cancel  context.CancelFunc
	done    chan error
}

func startHealth(ctx context.Context, opts Options, started time.Time) (context.Context, *healthHost, error) {
	ctx, cancel := context.WithCancel(ctx)
	h := &healthHost{cancel: cancel, now: time.Now, initial: healthResponse{
		TenantID: opts.TenantID, JobID: opts.JobID, StartedAt: started.UTC(),
		ActivitySnapshot: executionruntime.ActivitySnapshot{TaskType: "framework", CurrentTaskStartedAt: started.UTC()},
	}}
	if opts.HealthPort == 0 {
		return ctx, h, nil
	}
	listen := opts.healthListen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp4", "0.0.0.0:"+strconv.Itoa(opts.HealthPort))
	if err != nil {
		cancel()
		return ctx, nil, fmt.Errorf("listen for run health: %w", err)
	}
	h.server = &http.Server{Handler: h, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 * 1024}
	h.done = make(chan error, 1)
	go func() {
		err := h.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if err != nil {
			cancel()
		}
		h.done <- err
	}()
	return ctx, h, nil
}

func (h *healthHost) attach(runtime *executionruntime.Runtime) {
	if h.server == nil {
		return
	}
	runtime.EnableActivity(h.initial.StartedAt)
	key := jobdb.JobKey{TenantId: h.initial.TenantID, JobId: h.initial.JobID}
	h.mu.Lock()
	h.source = func() executionruntime.ActivitySnapshot { return runtime.ActivitySnapshot(key) }
	h.mu.Unlock()
}

func (h *healthHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/health" {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	response := h.initial
	h.mu.RLock()
	source := h.source
	h.mu.RUnlock()
	if source != nil {
		response.ActivitySnapshot = source()
	}
	response.ObservedAt = h.now().UTC()
	_ = json.NewEncoder(w).Encode(response)
}

func (h *healthHost) close(runErr error) error {
	h.cancel()
	if h.server == nil {
		return runErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.server.Shutdown(ctx); err != nil {
		_ = h.server.Close()
	}
	if err := <-h.done; err != nil {
		return exitError{code: exitCodeFailure, err: errors.Join(runErr, fmt.Errorf("serve run health: %w", err))}
	}
	return runErr
}
