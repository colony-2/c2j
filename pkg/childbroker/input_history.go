package childbroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func (s *Server) handleInputHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, e error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(jobinputs.Diagnostic("storage", "history request failed", e))
	}
	if r.Method != http.MethodPost {
		fail(405, jobinputs.Diagnostic("invalid_request", "POST required", nil))
		return
	}
	if !s.authorized(r) {
		fail(401, jobinputs.Diagnostic("denied", "helper authentication denied", jobdb.ErrLeasePermissionDenied))
		return
	}
	var q jobinputs.Request
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
		fail(400, jobinputs.Diagnostic("invalid_request", "invalid history request fields", nil))
		return
	}
	if e := q.Validate(); e != nil {
		fail(400, e)
		return
	}
	artifact := strings.HasSuffix(r.URL.Path, "/artifact")
	if artifact != (q.Operation == "artifact") {
		fail(400, jobinputs.Diagnostic("invalid_request", "history operation does not match route", nil))
		return
	}
	s.mu.Lock()
	api := s.inputs
	s.mu.Unlock()
	if api == nil {
		fail(501, jobinputs.Diagnostic("unsupported", "history helper unavailable; upgrade worker and CLI together", nil))
		return
	}
	if q.JobID == "self" {
		q.JobID = s.current.JobID
	}
	ctx, cancel := context.WithCancelCause(s.executionContext)
	defer cancel(nil)
	stop := context.AfterFunc(r.Context(), func() { cancel(context.Cause(r.Context())) })
	defer stop()
	// AfterFunc runs asynchronously; an already-ended request must not enter
	// the history API while its cancellation callback is still pending.
	if cause := context.Cause(r.Context()); cause != nil {
		cancel(cause)
	}
	if cause := context.Cause(ctx); cause != nil {
		fail(400, cause)
		return
	}
	if artifact {
		reader, e := api.Open(ctx, q)
		if e != nil {
			fail(400, e)
			return
		}
		if reader == nil {
			fail(500, jobinputs.Diagnostic("storage", "history member stream missing", nil))
			return
		}
		defer reader.Close()
		if reader.SizeBytes < 0 {
			fail(500, jobinputs.Diagnostic("size_mismatch", "negative retained member size", nil))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-C2J-Download-Version", "1")
		w.Header().Set("X-C2J-Download-Size", strconv.FormatInt(reader.SizeBytes, 10))
		w.Header().Set("Trailer", "X-C2J-Download-Error")
		_, e = io.Copy(w, reader)
		e = errors.Join(e, reader.Close())
		if e == nil {
			e = api.Check(ctx, q.JobID)
		}
		if e != nil {
			diagnostic := jobinputs.Diagnostic("read", "retained stream incomplete", e)
			diagnostic.JobID = q.JobID
			diagnostic.EntryID = q.EntryID
			diagnostic.ArtifactID = q.ArtifactID
			raw, _ := json.Marshal(diagnostic)
			w.Header().Set("X-C2J-Download-Error", base64.RawURLEncoding.EncodeToString(raw))
		}
		return
	}
	out, e := api.Inspect(ctx, q)
	if e != nil {
		fail(400, e)
		return
	}
	if e = out.Validate(q.Operation); e != nil {
		fail(500, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (c JobClient) Inputs(ctx context.Context, q jobinputs.Request) (*jobinputs.Response, error) {
	if e := q.Validate(); e != nil {
		return nil, e
	}
	response, e := c.callPayload(ctx, q, "/inspect/inputs", true)
	if e != nil {
		return nil, jobinputs.Diagnostic("transport", "history helper unavailable", e)
	}
	out, e := jobinputs.DecodeResponse(response.Body, q.Operation)
	e = errors.Join(e, response.Body.Close())
	if e != nil {
		return nil, jobinputs.Diagnostic("protocol", "history metadata failed", e)
	}
	return out, nil
}
func (c JobClient) InputArtifact(ctx context.Context, q jobinputs.Request) (io.ReadCloser, error) {
	if e := q.Validate(); e != nil {
		return nil, e
	}
	response, e := c.callPayload(ctx, q, "/inspect/inputs/artifact", true)
	if e != nil {
		return nil, jobinputs.Diagnostic("transport", "history helper unavailable", e)
	}
	if response.Header.Get("X-C2J-Download-Version") != "1" || response.Header.Get("Content-Type") != "application/octet-stream" {
		_ = response.Body.Close()
		return nil, jobinputs.Diagnostic("unsupported", "verified history download requires version 1; upgrade worker and CLI together", nil)
	}
	sizes := response.Header.Values("X-C2J-Download-Size")
	if len(sizes) != 1 {
		_ = response.Body.Close()
		return nil, jobinputs.Diagnostic("protocol", "history download requires one retained size", nil)
	}
	size, e := strconv.ParseInt(sizes[0], 10, 64)
	if e != nil || size < 0 || strconv.FormatInt(size, 10) != sizes[0] {
		_ = response.Body.Close()
		return nil, jobinputs.Diagnostic("protocol", "invalid history retained size", e)
	}
	return &inputDownload{ctx: ctx, response: response, size: size}, nil
}

type inputDownload struct {
	ctx         context.Context
	response    *http.Response
	size, count int64
	terminal    error
	done        bool
	once        sync.Once
	closeErr    error
	closeOnce   sync.Once
	result      error
}

func (b *inputDownload) closeSource() error {
	b.once.Do(func() { b.closeErr = b.response.Body.Close() })
	return b.closeErr
}
func (b *inputDownload) Read(p []byte) (int, error) {
	if b.done {
		return 0, b.terminal
	}
	n, e := b.response.Body.Read(p)
	b.count += int64(n)
	if b.count > b.size {
		e = jobinputs.Diagnostic("size_mismatch", "history download exceeds retained size", e)
	}
	if e != nil {
		if e == io.EOF {
			trailer := b.response.Trailer
			faults := trailer.Values("X-C2J-Download-Error")
			switch {
			case len(faults) > 0:
				e = jobinputs.Diagnostic("protocol", "invalid history error trailer", nil)
				if len(faults) == 1 {
					if raw, x := base64.RawURLEncoding.DecodeString(faults[0]); x == nil {
						if diagnostic, x := jobinputs.DecodeError(raw); x == nil {
							e = diagnostic
						}
					}
				}
			case b.count != b.size:
				e = jobinputs.Diagnostic("size_mismatch", "history download shorter than retained size", nil)
			}
		} else {
			e = jobinputs.Diagnostic("transport", "history connection incomplete", e)
		}
		if closeErr := b.closeSource(); closeErr != nil {
			if e == io.EOF {
				e = closeErr
			} else {
				e = errors.Join(e, closeErr)
			}
		}
		if cause := context.Cause(b.ctx); cause != nil {
			e = jobinputs.Diagnostic("canceled", "history download canceled", cause)
		}
		b.done = true
		b.terminal = e
	}
	return n, e
}
func (b *inputDownload) Close() error {
	b.closeOnce.Do(func() {
		e := b.closeSource()
		if !b.done {
			b.result = jobinputs.Diagnostic("protocol", "history download closed before HTTP EOF", e)
			return
		}
		if b.terminal != io.EOF {
			b.result = errors.Join(b.terminal, e)
			return
		}
		if cause := context.Cause(b.ctx); cause != nil {
			b.result = jobinputs.Diagnostic("canceled", "history download canceled", cause)
			return
		}
		b.result = e
	})
	return b.result
}
