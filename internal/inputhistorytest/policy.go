package inputhistorytest

import (
	"context"
	"sync"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Policy is a deterministic backend authorization and stream-fault boundary.
// It retains the real SQLite/remote implementation for all unfaulted calls.
type Policy struct {
	jobdb.ConsumerRuntime
	mu     sync.Mutex
	denied map[string]bool
	fault  func(context.Context, jobdb.ArtifactRef, jobdb.ArtifactReader) (jobdb.ArtifactReader, error)
	Opens  []jobdb.ArtifactRef
}

func (p *Policy) Deny(job string, deny bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.denied == nil {
		p.denied = map[string]bool{}
	}
	p.denied[job] = deny
}
func (p *Policy) Fault(f func(context.Context, jobdb.ArtifactRef, jobdb.ArtifactReader) (jobdb.ArtifactReader, error)) {
	p.mu.Lock()
	p.fault = f
	p.mu.Unlock()
}
func (p *Policy) GetJob(ctx context.Context, key jobdb.JobKey) (jobdb.JobInfo, error) {
	p.mu.Lock()
	denied := p.denied[key.JobId]
	p.mu.Unlock()
	if denied {
		return jobdb.JobInfo{}, jobdb.ErrLeasePermissionDenied
	}
	return p.ConsumerRuntime.GetJob(ctx, key)
}
func (p *Policy) OpenArtifact(ctx context.Context, ref jobdb.ArtifactRef) (jobdb.ArtifactReader, error) {
	p.mu.Lock()
	denied := p.denied[ref.JobKey.JobId]
	fault := p.fault
	p.Opens = append(p.Opens, ref)
	p.mu.Unlock()
	if denied {
		return nil, jobdb.ErrLeasePermissionDenied
	}
	a, e := p.ConsumerRuntime.OpenArtifact(ctx, ref)
	if e != nil {
		return nil, e
	}
	if fault != nil {
		return fault(ctx, ref, a)
	}
	return a, nil
}
func (p *Policy) Opened() []jobdb.ArtifactRef {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]jobdb.ArtifactRef(nil), p.Opens...)
}
