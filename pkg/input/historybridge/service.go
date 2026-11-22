// Package historybridge adapts the native retained history service to task authority.
package historybridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"sync"

	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/c2j/pkg/workflowctl"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type Runtime interface {
	jobdb.JobRunReader
	OpenArtifact(context.Context, jobdb.ArtifactRef) (jobdb.ArtifactReader, error)
}
type WaitReader interface {
	GetWaitingTask(context.Context, jobdb.JobKey) (workflowctl.TaskHandle, error)
}

func Factory(rt Runtime, wait WaitReader) jobinputs.Factory {
	return func(a jobinputs.Authority) (jobinputs.API, error) { return New(rt, wait, a, input.HistoryConfig{}) }
}

type Service struct {
	rt        Runtime
	authority jobinputs.Authority
	history   input.HistoryService
}

func New(rt Runtime, wait WaitReader, a jobinputs.Authority, cfg input.HistoryConfig) (*Service, error) {
	s := &Service{rt: rt, authority: a}
	cfg.Reader = &reader{rt: rt, wait: wait, authority: a}
	h, e := input.NewHistoryService(cfg)
	s.history = h
	return s, e
}
func (s *Service) Check(ctx context.Context, job string) error {
	if e := s.authority.Guard(ctx, job); e != nil {
		return e
	}
	_, e := s.rt.GetJob(ctx, jobdb.JobKey{TenantId: s.authority.Tenant, JobId: job})
	if e != nil {
		return e
	}
	return s.authority.Guard(ctx, job)
}

type fatalKey struct{}
type fatal struct {
	mu  sync.Mutex
	err error
}

func record(ctx context.Context, e error) {
	if e == nil || e == io.EOF {
		return
	}
	authorityFailure := errors.Is(e, jobdb.ErrLeasePermissionDenied) || errors.Is(e, jobdb.ErrExecutionLeaseLost) || errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded)
	if !authorityFailure && (errors.Is(e, fs.ErrNotExist) || errors.Is(e, jobdb.ErrChapterNotFound) || errors.Is(e, jobdb.ErrJobNotFound)) {
		return
	}
	if f, _ := ctx.Value(fatalKey{}).(*fatal); f != nil {
		f.mu.Lock()
		if f.err == nil {
			f.err = e
		}
		f.mu.Unlock()
	}
}
func (s *Service) begin(ctx context.Context, q jobinputs.Request) (context.Context, context.CancelFunc, *fatal, error) {
	if e := q.Validate(); e != nil {
		return ctx, func() {}, nil, e
	}
	ctx, cancel := s.authority.Bind(ctx)
	f := &fatal{}
	ctx = context.WithValue(ctx, fatalKey{}, f)
	if e := s.Check(ctx, q.JobID); e != nil {
		cancel()
		return ctx, func() {}, f, e
	}
	return ctx, cancel, f, nil
}
func (s *Service) Inspect(ctx context.Context, q jobinputs.Request) (*jobinputs.Response, error) {
	ctx, cancel, f, e := s.begin(ctx, q)
	defer cancel()
	if e != nil {
		return nil, mapped(q, e)
	}
	out := &jobinputs.Response{Version: 1}
	switch q.Operation {
	case "list":
		var p *input.JobInputStoryPage
		p, e = s.history.ListJobInputStory(ctx, input.ListJobInputStoryRequest{TenantID: s.authority.Tenant, JobID: q.JobID, PageSize: q.Limit, PageToken: q.Cursor})
		if e == nil {
			out.Page, e = json.Marshal(p)
		}
	case "show":
		var d *input.JobInputStoryEntryDetail
		d, e = s.history.GetJobInputStoryEntry(ctx, input.GetJobInputStoryEntryRequest{TenantID: s.authority.Tenant, JobID: q.JobID, EntryID: q.EntryID, SnapshotToken: q.Snapshot})
		if e == nil {
			out.Entry, e = json.Marshal(d)
		}
	default:
		e = jobinputs.Diagnostic("invalid_request", "metadata requires list or show", nil)
	}
	f.mu.Lock()
	fatalErr := f.err
	f.mu.Unlock()
	e = errors.Join(fatalErr, e, s.authority.Guard(ctx, q.JobID))
	if e != nil {
		return nil, mapped(q, e)
	}
	return out, nil
}
func (s *Service) Open(ctx context.Context, q jobinputs.Request) (*jobinputs.Artifact, error) {
	ctx, cancel, _, e := s.begin(ctx, q)
	if e != nil {
		cancel()
		return nil, mapped(q, e)
	}
	if q.Operation != "artifact" {
		cancel()
		return nil, mapped(q, jobinputs.Diagnostic("invalid_request", "artifact operation required", nil))
	}
	a, e := s.history.OpenJobInputStoryArtifact(ctx, input.OpenJobInputStoryArtifactRequest{TenantID: s.authority.Tenant, JobID: q.JobID, EntryID: q.EntryID, ArtifactID: q.ArtifactID, SnapshotToken: q.Snapshot})
	if e != nil {
		cancel()
		return nil, mapped(q, e)
	}
	return &jobinputs.Artifact{ReadCloser: &owned{ReadCloser: a, cancel: cancel, check: func() error { return s.authority.Guard(ctx, q.JobID) }}, SizeBytes: a.SizeBytes}, nil
}

type owned struct {
	io.ReadCloser
	cancel context.CancelFunc
	check  func() error
	once   sync.Once
	err    error
}

func (o *owned) Read(p []byte) (int, error) {
	if e := o.check(); e != nil {
		return 0, jobinputs.Diagnostic("canceled", "retained member authority ended", e)
	}
	return o.ReadCloser.Read(p)
}
func (o *owned) Close() error {
	o.once.Do(func() { o.err = o.ReadCloser.Close(); o.cancel() })
	return o.err
}
func mapped(q jobinputs.Request, e error) *jobinputs.Error {
	code := "storage"
	switch {
	case errors.Is(e, input.ErrHistoryUnsupported):
		code = "unsupported"
	case errors.Is(e, input.ErrHistoryInvalidCursor):
		code = "invalid_cursor"
	case errors.Is(e, input.ErrHistorySnapshotExpired):
		code = "snapshot_expired"
	case errors.Is(e, input.ErrHistoryEntryNotFound):
		code = "entry_not_found"
	case errors.Is(e, input.ErrHistoryArtifactNotFound):
		code = "nonmember"
	case errors.Is(e, input.ErrArtifactUnavailable), errors.Is(e, fs.ErrNotExist), errors.Is(e, jobdb.ErrChapterNotFound):
		code = "unavailable"
	}
	d := jobinputs.Diagnostic(code, "retained input history operation failed", e)
	d.JobID = q.JobID
	d.EntryID = q.EntryID
	d.ArtifactID = q.ArtifactID
	return d
}
