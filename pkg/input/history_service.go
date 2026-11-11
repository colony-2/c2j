package input

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type historySnapshot struct {
	merges      []HistoryMergeEntry
	events      []HistoryEvent
	highWater   int64
	key         jobdb.JobKey
	created     time.Time
	entries     []JobInputStoryEntryDetail
	diagnostics []HistoryDiagnostic
}
type historyService struct {
	secret     [32]byte
	issuer     string
	mapNode    func(context.Context, jobdb.JobKey, HistoryLocator) (string, error)
	mapRestart func(context.Context, jobdb.JobKey, HistoryLocator) (HistoryRestartAssociation, error)
	reader     HistoryReader
	ttl        time.Duration
	max        int
	mu         sync.Mutex
	snapshots  map[string]*historySnapshot
}

// NewHistoryService constructs an independent retained-history reader. It does
// not modify Runtime or story.Service, and needs no operation registry.
func NewHistoryService(cfg HistoryConfig) (HistoryService, error) {
	if cfg.Reader == nil {
		return nil, fmt.Errorf("history reader must be provided")
	}
	if cfg.SnapshotTTL < 0 || cfg.MaxSnapshots < 0 {
		return nil, fmt.Errorf("invalid history cache limits")
	}
	if cfg.SnapshotTTL == 0 {
		cfg.SnapshotTTL = 5 * time.Minute
	}
	if cfg.MaxSnapshots == 0 {
		cfg.MaxSnapshots = 128
	}
	s := &historyService{reader: cfg.Reader, mapNode: cfg.MapNode, mapRestart: cfg.MapRestart, ttl: cfg.SnapshotTTL, max: cfg.MaxSnapshots, snapshots: map[string]*historySnapshot{}}
	if _, err := rand.Read(s.secret[:]); err != nil {
		return nil, err
	}
	issuer := sha256.Sum256(s.secret[:])
	s.issuer = base64.RawURLEncoding.EncodeToString(issuer[:12])
	return s, nil
}

type historyCursor struct {
	Version               int    `json:"v"`
	Issuer                string `json:"issuer"`
	Tenant, Job, Snapshot string
	LastKey               string
	Revision              int64
	Offset                int
}

func (s *historyService) historyToken(c historyCursor) string {
	c.Issuer = s.issuer
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.secret[:])
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *historyService) parseHistoryToken(token string, key jobdb.JobKey) (historyCursor, error) {
	var c historyCursor
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return c, &HistoryError{Kind: ErrHistoryInvalidCursor}
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	sig, sigErr := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || sigErr != nil || len(sig) != sha256.Size || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.Tenant != key.TenantId || c.Job != key.JobId || c.Snapshot == "" || c.Offset < 0 || c.Issuer == "" {
		return c, &HistoryError{Kind: ErrHistoryInvalidCursor}
	}
	// An unknown issuer cannot access this instance's cache. Treat it only as
	// expired; no payload is trusted and no storage access follows this branch.
	if c.Issuer != s.issuer {
		return c, &HistoryError{Kind: ErrHistorySnapshotExpired}
	}
	mac := hmac.New(sha256.New, s.secret[:])
	mac.Write(b)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return c, &HistoryError{Kind: ErrHistoryInvalidCursor}
	}
	return c, nil
}
func historyKey(tenant, job string) (jobdb.JobKey, error) {
	if tenant == "" || job == "" {
		return jobdb.JobKey{}, &HistoryError{Kind: ErrHistoryInvalidCursor, Cause: fmt.Errorf("tenant and job are required")}
	}
	return jobdb.JobKey{TenantId: tenant, JobId: job}, nil
}
func (s *historyService) snapshot(ctx context.Context, key jobdb.JobKey, token string) (*historySnapshot, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if token != "" {
		c, err := s.parseHistoryToken(token, key)
		if err != nil {
			return nil, "", err
		}
		if c.Offset != 0 {
			return nil, "", &HistoryError{Kind: ErrHistoryInvalidCursor}
		}
		s.mu.Lock()
		snap := s.snapshots[c.Snapshot]
		s.mu.Unlock()
		if snap == nil || time.Since(snap.created) >= s.ttl {
			return nil, "", &HistoryError{Kind: ErrHistorySnapshotExpired}
		}
		if snap.key != key || snap.highWater != c.Revision {
			return nil, "", &HistoryError{Kind: ErrHistoryInvalidCursor}
		}
		return snap, token, nil
	}
	run, err := s.reader.GetJobRun(ctx, jobdb.GetJobRunRequest{JobKey: key, IncludeInputs: true, IncludeOutputs: true, IncludeArtifacts: true, IncludeAttemptInputs: true})
	if err != nil {
		return nil, "", err
	}
	if run.Job.JobKey != key {
		return nil, "", jobdb.ErrJobNotFound
	}
	entries, diagnostics, err := s.project(ctx, key, run)
	if err != nil {
		return nil, "", err
	}
	var high int64
	for _, e := range run.Execution {
		high = max(high, e.Ordinal)
	}
	snap := &historySnapshot{highWater: high, key: key, created: time.Now().UTC(), entries: historyCopy(entries), diagnostics: historyCopy(diagnostics)}
	snap.merges = historyCopy(historyMergeProjection(key, run))
	snap.events = historyEvents(entries, snap.merges, run)
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, "", err
	}
	id := base64.RawURLEncoding.EncodeToString(nonce[:])
	s.mu.Lock()
	for k, v := range s.snapshots {
		if time.Since(v.created) >= s.ttl {
			delete(s.snapshots, k)
		}
	}
	if len(s.snapshots) >= s.max {
		var oldest string
		for k, v := range s.snapshots {
			if oldest == "" || v.created.Before(s.snapshots[oldest].created) {
				oldest = k
			}
		}
		delete(s.snapshots, oldest)
	}
	s.snapshots[id] = snap
	s.mu.Unlock()
	return snap, s.historyToken(historyCursor{Version: 1, Tenant: key.TenantId, Job: key.JobId, Snapshot: id, Revision: high}), nil
}

// Copies prevent callers from mutating cached pages, raw fields or manifests.
func historyCopy[T any](in T) T {
	b, _ := json.Marshal(in)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}
func historyCompleteness(d []HistoryDiagnostic) string {
	if len(d) > 0 {
		return "partial"
	}
	return "complete"
}

func (s *historyService) ListJobInputStory(ctx context.Context, req ListJobInputStoryRequest) (*JobInputStoryPage, error) {
	key, err := historyKey(req.TenantID, req.JobID)
	if err != nil {
		return nil, err
	}
	size := req.PageSize
	if size == 0 {
		size = 50
	}
	if size < 1 || size > 500 {
		return nil, &HistoryError{Kind: ErrHistoryInvalidCursor, Cause: fmt.Errorf("page size must be 1..500")}
	}
	offset := 0
	lastKey := ""
	token := ""
	if req.PageToken != "" {
		c, e := s.parseHistoryToken(req.PageToken, key)
		if e != nil {
			return nil, e
		}
		offset = c.Offset
		lastKey = c.LastKey
		c.Offset = 0
		c.LastKey = ""
		token = s.historyToken(c)
	}
	snap, token, err := s.snapshot(ctx, key, token)
	if err != nil {
		return nil, err
	}
	if offset > len(snap.entries) || (offset > 0 && snap.entries[offset-1].EntryID != lastKey) {
		return nil, &HistoryError{Kind: ErrHistoryInvalidCursor}
	}
	end := min(offset+size, len(snap.entries))
	page := &JobInputStoryPage{TenantID: key.TenantId, JobID: key.JobId, SnapshotRevision: snap.highWater, SnapshotToken: token, ObservedAt: snap.created, Completeness: historyCompleteness(snap.diagnostics), Diagnostics: historyCopy(snap.diagnostics), Entries: []JobInputStoryEntry{}}
	for _, e := range snap.entries[offset:end] {
		page.Entries = append(page.Entries, historyCopy(e.JobInputStoryEntry))
	}
	if end < len(snap.entries) {
		c, _ := s.parseHistoryToken(token, key)
		c.Offset = end
		c.LastKey = snap.entries[end-1].EntryID
		page.NextPageToken = s.historyToken(c)
	}
	return page, nil
}

func (s *historyService) detail(ctx context.Context, req GetJobInputStoryEntryRequest, live bool) (*JobInputStoryEntryDetail, error) {
	key, err := historyKey(req.TenantID, req.JobID)
	if err != nil {
		return nil, err
	}
	snap, token, err := s.snapshot(ctx, key, req.SnapshotToken)
	if err != nil {
		return nil, err
	}
	for _, entry := range snap.entries {
		if entry.EntryID != req.EntryID {
			continue
		}
		d := historyCopy(entry)
		d.SnapshotToken = token
		d.SnapshotRevision = snap.highWater
		d.ObservedAt = snap.created
		d.Completeness = historyCompleteness(snap.diagnostics)
		d.Diagnostics = historyCopy(snap.diagnostics)
		if live {
			d.Actionability = s.observe(ctx, key, d)
		}
		return &d, nil
	}
	return nil, &HistoryError{Kind: ErrHistoryEntryNotFound, EntryID: req.EntryID}
}
func (s *historyService) GetJobInputStoryEntry(ctx context.Context, req GetJobInputStoryEntryRequest) (*JobInputStoryEntryDetail, error) {
	return s.detail(ctx, req, true)
}
func (s *historyService) OpenJobInputStoryArtifact(ctx context.Context, req OpenJobInputStoryArtifactRequest) (*HistoryArtifact, error) {
	d, err := s.detail(ctx, GetJobInputStoryEntryRequest{TenantID: req.TenantID, JobID: req.JobID, EntryID: req.EntryID, SnapshotToken: req.SnapshotToken}, false)
	if err != nil {
		return nil, err
	}
	for _, manifest := range [][]HistoryArtifactMember{d.RequestManifest, d.ResponseManifest} {
		for _, member := range manifest {
			if member.ArtifactID != req.ArtifactID {
				continue
			}
			key, ok := member.Ref.StoredKey()
			if !ok || member.Availability != "retained" {
				return nil, &HistoryError{Kind: ErrArtifactUnavailable, EntryID: req.EntryID, ArtifactID: req.ArtifactID}
			}
			a := s.reader.GetArtifactLazy(ctx, req.TenantID, key)
			if a == nil {
				return nil, &HistoryError{Kind: ErrArtifactUnavailable, EntryID: req.EntryID, ArtifactID: req.ArtifactID}
			}
			r, e := a.Open()
			if e != nil {
				return nil, documentReadError(req.ArtifactID, e)
			}
			return &HistoryArtifact{ArtifactID: member.ArtifactID, Ref: member.Ref, Name: member.Ref.NameValue(), SizeBytes: key.SizeBytes, ReadCloser: &reviewDocumentReader{ReadCloser: r, ctx: ctx, id: member.ArtifactID}}, nil
		}
	}
	return nil, &HistoryError{Kind: ErrHistoryArtifactNotFound, EntryID: req.EntryID, ArtifactID: req.ArtifactID}
}

func historySort(entries []JobInputStoryEntryDetail, ordinals map[string]int64) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := ordinals[entries[i].EntryID], ordinals[entries[j].EntryID]
		if a != b {
			return a < b
		}
		ai, aj := entries[i].Locator.InvocationID, entries[j].Locator.InvocationID
		if ai != aj {
			return ai < aj
		}
		ci, cj := strings.Join(entries[i].Locator.CollectionInvocationIDs, "|"), strings.Join(entries[j].Locator.CollectionInvocationIDs, "|")
		if ci != cj {
			return ci < cj
		}
		return entries[i].EntryID < entries[j].EntryID
	})
}
