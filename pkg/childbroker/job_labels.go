package childbroker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/colony-2/c2j/internal/joblabelscope"
	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/joblabel"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

const labelEditPath = "/labels/edit"
const labelTokenHeader = "X-C2J-Job-Label-Token"

type labelSession struct {
	ctx   context.Context
	token string
	edit  joblabelscope.EditFunc
}

func (server *Server) BeginLabelSession(ctx context.Context, edit joblabelscope.EditFunc) (map[string]string, func(), error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.executionContext.Err() != nil || ctx.Err() != nil || edit == nil || server.labels != nil {
		return nil, nil, jobdb.ErrLeasePermissionDenied
	}
	token, err := randomHex(32)
	if err != nil {
		return nil, nil, err
	}
	active, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(server.executionContext, cancel)
	session := &labelSession{ctx: active, token: token, edit: edit}
	server.labels = session
	return map[string]string{jobcontext.ChildJobLabelTokenEnv: token}, func() {
		cancel()
		stop()
		server.mu.Lock()
		defer server.mu.Unlock()
		if server.labels == session {
			server.labels = nil
		}
	}, nil
}

func (server *Server) handleLabelEdit(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	server.mu.Lock()
	session := server.labels
	server.mu.Unlock()
	if !server.authorized(request) || session == nil || session.ctx.Err() != nil || server.executionContext.Err() != nil || subtle.ConstantTimeCompare([]byte(request.Header.Get(labelTokenHeader)), []byte(session.token)) != 1 {
		writeLabelError(writer, jobdb.ErrLeasePermissionDenied)
		return
	}
	controller := http.NewResponseController(writer)
	_ = controller.SetReadDeadline(time.Now().Add(5 * time.Second))
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 8<<10))
	var edit joblabel.Edit
	if decoder.Decode(&edit) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		writeLabelError(writer, joblabel.ErrInvalidEdit)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 10*time.Second)
	defer cancel()
	stop := context.AfterFunc(session.ctx, cancel)
	defer stop()
	if session.ctx.Err() != nil {
		writeLabelError(writer, jobdb.ErrExecutionLeaseLost)
		return
	}
	state, err := session.edit(ctx, edit)
	if err != nil {
		writeLabelError(writer, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(state)
}

var labelErrors = []struct {
	code   string
	status int
	err    error
}{
	{"denied", http.StatusForbidden, jobdb.ErrLeasePermissionDenied},
	{"conflict", http.StatusConflict, jobdb.ErrConflict},
	{"not_found", http.StatusNotFound, jobdb.ErrJobNotFound},
	{"lease_lost", http.StatusGone, jobdb.ErrExecutionLeaseLost},
	{"unsupported", http.StatusNotImplemented, jobdb.ErrJobMetadataUnsupported},
	{"invalid_edit", http.StatusBadRequest, joblabel.ErrInvalidEdit},
	{"canceled", http.StatusRequestTimeout, context.Canceled},
	{"timed_out", http.StatusGatewayTimeout, context.DeadlineExceeded},
}

func writeLabelError(writer http.ResponseWriter, err error) {
	if errors.Is(err, jobdb.ErrCellForbidden) {
		err = jobdb.ErrLeasePermissionDenied
	}
	writer.Header().Set("Content-Type", "application/json")
	for _, category := range labelErrors {
		if errors.Is(err, category.err) {
			writer.WriteHeader(category.status)
			_ = json.NewEncoder(writer).Encode(jobdb.CellError{Code: category.code, Status: category.status})
			return
		}
	}
	writer.WriteHeader(http.StatusInternalServerError)
}

func (client JobClient) EditCurrentJobLabel(ctx context.Context, edit joblabel.Edit) (joblabel.State, error) {
	if err := edit.Validate(); err != nil {
		return joblabel.State{}, err
	}
	if client.Broker.LabelToken == "" {
		return joblabel.State{}, jobdb.ErrLeasePermissionDenied
	}
	response, err := client.callPayload(ctx, edit, labelEditPath, false)
	if err != nil {
		return joblabel.State{}, err
	}
	defer response.Body.Close()
	var state joblabel.State
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<10))
	if decoder.Decode(&state) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF || state.Status == "" || state.Version == nil {
		return joblabel.State{}, fmt.Errorf("invalid job label receipt")
	}
	return state, nil
}

func labelResponseError(status int, body []byte) error {
	var typed jobdb.CellError
	if json.Unmarshal(body, &typed) == nil && typed.Status == status {
		for _, category := range labelErrors {
			if category.status == status && category.code == typed.Code {
				return category.err
			}
		}
	}
	switch status {
	case http.StatusForbidden, http.StatusUnauthorized:
		return jobdb.ErrLeasePermissionDenied
	case http.StatusNotFound:
		return jobdb.ErrJobNotFound
	default:
		return fmt.Errorf("job label helper returned HTTP %d; edit outcome is unknown", status)
	}
}
