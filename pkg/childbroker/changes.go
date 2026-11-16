package childbroker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/colony-2/c2j/pkg/jobinspect"
	"github.com/colony-2/c2j/pkg/permissions"
)

const changesTerminal = "\n{\"version\":1,\"complete\":true}\n"

func isChangesPayload(q any) bool {
	switch q.(type) {
	case jobinspect.ChangesRequest, jobinspect.DiffRequest:
		return true
	}
	return false
}

func (s *Server) handleChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	fail := func(err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(jobinspect.ChangesDiagnostic(err))
	}
	api, ok := jobinspect.FromContext(s.executionContext).(jobinspect.ChangesAPI)
	if !ok {
		fail(&jobinspect.ChangesError{Version: 1, Code: "protocol", Detail: "worker does not support Changes"})
		return
	}
	ctx, cancel := context.WithCancel(s.executionContext)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	if access := permissions.FromContext(s.executionContext); access != nil {
		stop := context.AfterFunc(access.Context(), cancel)
		defer stop()
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if strings.HasSuffix(r.URL.Path, "/changes") {
		var q jobinspect.ChangesRequest
		if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
			fail(&jobinspect.ChangesError{Version: 1, Code: "invalid_request", Detail: "invalid Changes request"})
			return
		}
		result, err := api.History(ctx, q)
		if err != nil {
			fail(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	var q jobinspect.DiffRequest
	if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
		fail(&jobinspect.ChangesError{Version: 1, Code: "invalid_request", Detail: "invalid diff request"})
		return
	}
	meta, body, err := api.OpenDiff(ctx, q)
	if err != nil {
		fail(err)
		return
	}
	defer body.Close()
	header, err := json.Marshal(meta)
	if err != nil || len(header) > 128<<10 {
		fail(&jobinspect.ChangesError{Version: 1, Code: "protocol", Detail: "diff metadata exceeds transport limit"})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.c2j.diff-v1")
	if _, err = w.Write(append(header, '\n')); err != nil {
		return
	}
	n, err := io.Copy(w, body)
	closeErr := body.Close()
	if err != nil || closeErr != nil || n != meta.Bytes || ctx.Err() != nil {
		return
	}
	_, _ = io.WriteString(w, changesTerminal)
}

func (c JobClient) History(ctx context.Context, q jobinspect.ChangesRequest) (*jobinspect.ChangesHistory, error) {
	r, err := c.callPayload(ctx, q, "/inspect/changes", true)
	if err != nil {
		return nil, changesClientError(err)
	}
	defer r.Body.Close()
	var out jobinspect.ChangesHistory
	d := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	if err := d.Decode(&out); err != nil {
		return nil, changesMetadataReadError(ctx, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, changesMetadataReadError(ctx, err)
	}
	if out.Selection == "" || out.Commits == nil {
		return nil, changesTransport("invalid changes metadata")
	}
	if e := jobinspect.ValidateChangesHistory(&out); e != nil {
		return nil, e
	}
	if out.JobID != q.JobID || (q.Selection != "" && out.Selection != q.Selection) {
		return nil, &jobinspect.ChangesError{Version: 1, Code: "protocol", Detail: "history response does not match request"}
	}
	return &out, nil
}

func changesMetadataReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return changesClientError(ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return changesClientError(err)
	}
	return changesTransport("invalid changes metadata")
}

func (c JobClient) OpenDiff(ctx context.Context, q jobinspect.DiffRequest) (*jobinspect.ChangesDiff, io.ReadCloser, error) {
	r, err := c.callPayload(ctx, q, "/inspect/diff", true)
	if err != nil {
		return nil, nil, changesClientError(err)
	}
	reader := bufio.NewReaderSize(r.Body, 128<<10+1)
	header, err := reader.ReadSlice('\n')
	if err != nil && (ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		r.Body.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, changesClientError(err)
	}
	var meta jobinspect.ChangesDiff
	if err != nil || len(header) > 128<<10 || json.Unmarshal(header, &meta) != nil || meta.Bytes < 0 || !meta.Complete || meta.Selection == "" {
		r.Body.Close()
		return nil, nil, changesTransport("invalid diff metadata")
	}
	if e := jobinspect.ValidateChangesDiff(&meta); e != nil {
		r.Body.Close()
		return nil, nil, e
	}
	if meta.JobID != q.JobID || (q.Selection != "" && meta.Selection != q.Selection) {
		r.Body.Close()
		return nil, nil, &jobinspect.ChangesError{Version: 1, Code: "protocol", Detail: "diff response does not match request"}
	}
	return &meta, &changesBody{ctx: ctx, body: r.Body, reader: reader, remaining: meta.Bytes}, nil
}

func changesTransport(detail string) *jobinspect.ChangesError {
	return &jobinspect.ChangesError{Version: 1, Code: "transport", Detail: detail}
}

func changesClientError(err error) error {
	var diagnostic *jobinspect.ChangesError
	if errors.As(err, &diagnostic) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return jobinspect.ChangesDiagnostic(err)
	}
	return changesTransport("Changes helper request failed")
}

type changesBody struct {
	ctx       context.Context
	body      io.ReadCloser
	reader    *bufio.Reader
	remaining int64
	done      bool
	failure   error
}

func (b *changesBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.failure != nil {
		return 0, b.failure
	}
	if b.done {
		return 0, io.EOF
	}
	if err := b.ctx.Err(); err != nil {
		return 0, b.fail(err, "diff read canceled")
	}
	if b.remaining > 0 {
		if int64(len(p)) > b.remaining {
			p = p[:b.remaining]
		}
		n, err := b.reader.Read(p)
		b.remaining -= int64(n)
		if err != nil {
			return n, b.fail(err, "diff stream ended before declared length")
		}
		return n, nil
	}
	terminal := make([]byte, len(changesTerminal))
	_, err := io.ReadFull(b.reader, terminal)
	if err != nil || string(terminal) != changesTerminal {
		return 0, b.fail(err, "diff terminal status is missing")
	}
	if _, err = b.reader.ReadByte(); err != io.EOF {
		return 0, b.fail(err, "unexpected diff stream suffix")
	}
	if err := b.ctx.Err(); err != nil {
		return 0, b.fail(err, "diff read canceled")
	}
	b.done = true
	_ = b.body.Close()
	return 0, io.EOF
}

func (b *changesBody) fail(err error, detail string) error {
	if b.ctx.Err() != nil {
		err = b.ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		b.failure = jobinspect.ChangesDiagnostic(err)
	} else {
		b.failure = changesTransport(detail)
	}
	_ = b.body.Close()
	return b.failure
}

func (b *changesBody) Close() error { return b.body.Close() }

var _ jobinspect.ChangesAPI = JobClient{}
