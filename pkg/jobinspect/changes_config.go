package jobinspect

import (
	"context"
	"io"
	"math"
	"time"
)

// RepositoryAccess is returned by a trusted application resolver, never by the
// read request. URL and environment are used only for exact-object fetches.
// The resolver owns authorization, routing and credentials for this repository.
type RepositoryAccess struct {
	URL         string
	Environment []string
}

type ChangesLimits struct {
	CursorBytes         int64
	HeaderBytes         int64
	ArtifactBytes       int64
	DownloadBytes       int64 // Total retained artifact bytes plus received repository pack bytes per operation.
	LineageCandidates   int
	PrerequisiteSources int
	Depth               int
	Commits             int
	Objects             int
	ObjectBytes         int64
	InflatedBytes       int64
	TemporaryBytes      int64
	PatchBytes          int64
	PageSize            int
	OperationTime       time.Duration
	GitTime             time.Duration
}

func DefaultChangesLimits() ChangesLimits {
	return ChangesLimits{64 << 10, 64 << 10, 256 << 20, 512 << 20, 128, 32, 16, 10000, 100000, 64 << 20, 1 << 30, 2 << 30, 16 << 20, 500, 60 * time.Second, 30 * time.Second}
}

// ChangesConfig is immutable after construction. Nil ResolveRepository means
// offline, including no local repository fallback. TempDir must be private and
// writable. Zero limits use defaults; negative limits are invalid.
type ChangesConfig struct {
	TempDir           string
	GitPath           string
	Limits            ChangesLimits
	ResolveRepository func(context.Context, string) (RepositoryAccess, error)
}

type ChangesService struct {
	service *Service
	config  ChangesConfig
}

func NewChangesService(service *Service, config ChangesConfig) (*ChangesService, error) {
	if service == nil || service.Runtime == nil || service.Tenant == "" {
		return nil, changesErr("invalid_request", "runtime and authenticated tenant are required")
	}
	d := DefaultChangesLimits()
	l := &config.Limits
	for _, p := range []struct {
		v *int64
		d int64
	}{{&l.CursorBytes, d.CursorBytes}, {&l.HeaderBytes, d.HeaderBytes}, {&l.ArtifactBytes, d.ArtifactBytes}, {&l.DownloadBytes, d.DownloadBytes}, {&l.ObjectBytes, d.ObjectBytes}, {&l.InflatedBytes, d.InflatedBytes}, {&l.TemporaryBytes, d.TemporaryBytes}, {&l.PatchBytes, d.PatchBytes}} {
		if *p.v < 0 {
			return nil, changesErr("invalid_request", "negative changes budget")
		}
		if *p.v == 0 {
			*p.v = p.d
		}
	}
	for _, p := range []struct {
		v *int
		d int
	}{{&l.LineageCandidates, d.LineageCandidates}, {&l.PrerequisiteSources, d.PrerequisiteSources}, {&l.Depth, d.Depth}, {&l.Commits, d.Commits}, {&l.Objects, d.Objects}, {&l.PageSize, d.PageSize}} {
		if *p.v < 0 {
			return nil, changesErr("invalid_request", "negative changes budget")
		}
		if *p.v == 0 {
			*p.v = p.d
		}
	}
	if l.OperationTime < 0 || l.GitTime < 0 {
		return nil, changesErr("invalid_request", "negative time budget")
	}
	if l.OperationTime == 0 {
		l.OperationTime = d.OperationTime
	}
	if l.GitTime == 0 {
		l.GitTime = d.GitTime
	}
	if l.PageSize > 500 || l.ArtifactBytes > l.DownloadBytes || l.ObjectBytes > l.InflatedBytes || l.PatchBytes > l.TemporaryBytes {
		return nil, changesErr("invalid_request", "contradictory changes budgets")
	}
	if l.CursorBytes > 64<<20 || l.HeaderBytes > 64<<20 || l.Objects > math.MaxInt64/128 || l.DownloadBytes == math.MaxInt64 || l.ObjectBytes == math.MaxInt64 || l.PatchBytes == math.MaxInt64 {
		return nil, changesErr("invalid_request", "changes budgets exceed supported numeric bounds")
	}
	if config.GitPath == "" {
		config.GitPath = "git"
	}
	copy := *service
	return &ChangesService{&copy, config}, nil
}

// Service implements ChangesAPI with offline defaults, including in helpers.
func (s *Service) History(ctx context.Context, q ChangesRequest) (*ChangesHistory, error) {
	c, e := NewChangesService(s, s.ChangesConfig)
	if e != nil {
		return nil, e
	}
	return c.History(ctx, q)
}
func (s *Service) OpenDiff(ctx context.Context, q DiffRequest) (*ChangesDiff, io.ReadCloser, error) {
	c, e := NewChangesService(s, s.ChangesConfig)
	if e != nil {
		return nil, nil, e
	}
	return c.OpenDiff(ctx, q)
}
