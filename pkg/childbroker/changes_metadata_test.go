package childbroker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/stretchr/testify/require"
)

// Run after real writer/backend/helper reads in TestChangesHelperJ37J41L09.
func testChangesMetadataInterruptions(t *testing.T, ctx context.Context, h *jobinspect.ChangesHistory, q jobinspect.ChangesRequest) {
	historyJSON, err := json.Marshal(h)
	require.NoError(t, err)
	for _, phase := range []struct {
		name, prefix string
		status       int
		diff         bool
	}{
		{"history-partial", string(historyJSON[:len(historyJSON)/2]), 200, false},
		{"history-eof", string(historyJSON) + "\n", 200, false},
		{"history-diagnostic", `{"version":1,"code":"limit"`, 400, false},
		{"diff-diagnostic", `{"version":1,"code":"limit"`, 400, true},
	} {
		for _, deadline := range []bool{false, true} {
			name := "cancel-" + phase.name
			if deadline {
				name = "deadline-" + phase.name
			}
			t.Run(name, func(t *testing.T) {
				started := make(chan struct{})
				fault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(phase.status)
					_, _ = io.WriteString(w, phase.prefix)
					w.(http.Flusher).Flush()
					close(started)
					<-r.Context().Done()
				}))
				defer fault.Close()
				readCtx, cancel := context.WithCancel(ctx)
				want, code := context.Canceled, "canceled"
				if deadline {
					cancel()
					readCtx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
					want, code = context.DeadlineExceeded, "deadline"
				}
				defer cancel()
				client := JobClient{Broker: jobcontext.ChildJobBroker{Endpoint: fault.URL}}
				finished := make(chan error, 1)
				go func() {
					if phase.diff {
						meta, body, err := client.OpenDiff(readCtx, jobinspect.DiffRequest{ChangesRequest: q})
						if body != nil {
							body.Close()
						}
						if meta != nil || body != nil {
							err = io.ErrUnexpectedEOF
						}
						finished <- err
						return
					}
					out, err := client.History(readCtx, q)
					if out != nil {
						err = io.ErrUnexpectedEOF
					}
					finished <- err
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("metadata response never started")
				}
				if !deadline {
					cancel()
				}
				select {
				case err := <-finished:
					require.ErrorIs(t, err, want)
					require.Equal(t, code, jobinspect.ChangesDiagnostic(err).Code)
				case <-time.After(5 * time.Second):
					t.Fatal("interrupted metadata read did not release")
				}
			})
		}
	}
}
