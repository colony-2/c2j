package historybridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/colony-2/c2j/internal/celltest"
	"github.com/colony-2/c2j/internal/inputhistorytest"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/jobinputs"
	"github.com/colony-2/jobdb/pkg/jobdb"
	sqliteruntime "github.com/colony-2/jobdb/pkg/jobdb/runtime/sqlite"
	"github.com/stretchr/testify/require"
)

type policy struct {
	jobdb.ConsumerRuntime
	deny     atomic.Bool
	producer atomic.Bool
	opens    atomic.Int32
}

func (p *policy) GetJob(ctx context.Context, k jobdb.JobKey) (jobdb.JobInfo, error) {
	if p.deny.Load() && k.JobId == "history-job" || p.producer.Load() && k.JobId == "source-job" {
		return jobdb.JobInfo{}, jobdb.ErrLeasePermissionDenied
	}
	return p.ConsumerRuntime.GetJob(ctx, k)
}
func (p *policy) OpenArtifact(ctx context.Context, k jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	p.opens.Add(1)
	return p.ConsumerRuntime.OpenArtifact(ctx, k)
}
func authority() jobinputs.Authority {
	return jobinputs.Authority{Tenant: inputhistorytest.Tenant, Guard: func(c context.Context, _ string) error { return context.Cause(c) }, Bind: func(c context.Context) (context.Context, context.CancelFunc) { return context.WithCancel(c) }}
}
func bridgeFixture(t *testing.T) (context.Context, *inputhistorytest.Fixture, *policy) {
	t.Helper()
	ctx := celltest.Context(context.Background())
	rt, e := sqliteruntime.NewFromConfig(ctx, sqliteruntime.Config{DBPath: filepath.Join(t.TempDir(), "history.db"), MaxChapterApplicationPayloadBytes: 4 * 1024 * 1024})
	require.NoError(t, e)
	t.Cleanup(func() { require.NoError(t, rt.Close(ctx)) })
	f, e := inputhistorytest.Build(ctx, rt)
	require.NoError(t, e)
	return ctx, f, &policy{ConsumerRuntime: rt}
}
func TestInputHistoryAdapterSnapshotsAndAuthority(t *testing.T) {
	ctx, f, rt := bridgeFixture(t)
	require.NoError(t, f.Advance(ctx, 0))
	s, e := New(rt, f.Control, authority(), input.HistoryConfig{})
	require.NoError(t, e)
	list := jobinputs.Request{Version: 1, Operation: "list", JobID: f.Job.JobId, Limit: 1}
	response, e := s.Inspect(ctx, list)
	require.NoError(t, e)
	var page input.JobInputStoryPage
	require.NoError(t, json.Unmarshal(response.Page, &page))
	require.Len(t, page.Entries, 1)
	require.NotEmpty(t, page.NextPageToken)
	show := jobinputs.Request{Version: 1, Operation: "show", JobID: f.Job.JobId, EntryID: page.Entries[0].EntryID, Snapshot: page.SnapshotToken}
	d, e := s.Inspect(ctx, show)
	require.NoError(t, e)
	var detail input.JobInputStoryEntryDetail
	require.NoError(t, json.Unmarshal(d.Entry, &detail))
	require.Contains(t, string(detail.RawForm), "9007199254740993")
	require.Equal(t, "submission_receipt", detail.Actor.Provenance)
	require.NotEmpty(t, detail.ResponseManifest)
	var member input.HistoryArtifactMember
	for _, m := range detail.RequestManifest {
		if k, ok := m.Ref.StoredKey(); ok && k.JobId == f.Source.JobId && k.Name == f.Source.Name {
			member = m
		}
	}
	require.NotEmpty(t, member.ArtifactID)
	artifact := show
	artifact.Operation = "artifact"
	artifact.ArtifactID = member.ArtifactID
	bytes, e := s.Open(ctx, artifact)
	require.NoError(t, e)
	b, e := io.ReadAll(bytes)
	require.NoError(t, e)
	require.Equal(t, f.Binary, b)
	require.NoError(t, bytes.Close())
	rt.deny.Store(true)
	continuation := list
	continuation.Cursor = page.NextPageToken
	for _, q := range []jobinputs.Request{list, continuation, show, artifact} {
		if q.Operation == "artifact" {
			_, e = s.Open(ctx, q)
		} else {
			_, e = s.Inspect(ctx, q)
		}
		require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
	}
	rt.deny.Store(false)
	rt.producer.Store(true)
	_, e = s.Open(ctx, artifact)
	require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
	rt.producer.Store(false)
	before := rt.opens.Load()
	for _, q := range []jobinputs.Request{func() jobinputs.Request { q := artifact; q.ArtifactID = "raw-key"; return q }(), func() jobinputs.Request { q := artifact; q.EntryID = "forged"; return q }()} {
		_, e = s.Open(ctx, q)
		require.Error(t, e)
	}
	require.Equal(t, before, rt.opens.Load())
	require.NoError(t, f.Advance(ctx, 1))
	next, e := s.Inspect(ctx, continuation)
	require.NoError(t, e)
	var old input.JobInputStoryPage
	require.NoError(t, json.Unmarshal(next.Page, &old))
	require.Len(t, old.Entries, 1)
	require.Equal(t, "pending", old.Entries[0].State)
	require.Empty(t, old.NextPageToken)
	fresh, e := s.Inspect(ctx, list)
	require.NoError(t, e)
	require.NotEqual(t, response.Page, fresh.Page)
	// Native token types remain distinct across coordinates and issuers.
	other, e := New(rt, f.Control, authority(), input.HistoryConfig{})
	require.NoError(t, e)
	_, e = other.Inspect(ctx, show)
	require.ErrorIs(t, e, input.ErrHistorySnapshotExpired)
	bad := show
	bad.Snapshot += "tamper"
	_, e = s.Inspect(ctx, bad)
	require.ErrorIs(t, e, input.ErrHistoryInvalidCursor)
	bad = show
	bad.JobID = "source-job"
	_, e = s.Inspect(ctx, bad)
	require.ErrorIs(t, e, input.ErrHistoryInvalidCursor)
}
func TestInputHistoryAdapterExpiryAndEviction(t *testing.T) {
	ctx, f, rt := bridgeFixture(t)
	for _, cfg := range []input.HistoryConfig{{MaxSnapshots: 1}, {SnapshotTTL: time.Nanosecond}} {
		s, e := New(rt, f.Control, authority(), cfg)
		require.NoError(t, e)
		q := jobinputs.Request{Version: 1, Operation: "list", JobID: f.Job.JobId}
		p, e := s.Inspect(ctx, q)
		require.NoError(t, e)
		var page input.JobInputStoryPage
		require.NoError(t, json.Unmarshal(p.Page, &page))
		_, e = s.Inspect(ctx, q)
		require.NoError(t, e)
		_, e = s.Inspect(ctx, jobinputs.Request{Version: 1, Operation: "show", JobID: q.JobID, EntryID: page.Entries[0].EntryID, Snapshot: page.SnapshotToken})
		require.ErrorIs(t, e, input.ErrHistorySnapshotExpired)
	}
}
func TestInputHistoryErrorCauses(t *testing.T) {
	q := jobinputs.Request{JobID: "j", EntryID: "e", ArtifactID: "m"}
	e := mapped(q, &input.HistoryError{Kind: input.ErrArtifactUnavailable, Cause: jobdb.ErrLeasePermissionDenied})
	require.Equal(t, "denied", e.Code)
	require.ErrorIs(t, e, jobdb.ErrLeasePermissionDenied)
	require.Equal(t, "m", e.ArtifactID)
	sentinel := errors.New("read fault")
	e = mapped(q, jobinputs.Diagnostic("read", "read failed", sentinel))
	require.Equal(t, "read", e.Code)
	require.ErrorIs(t, e, sentinel)
}
