package permissions

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

type ResolveFunc func(context.Context, []string) (jobdb.SecretResolveResult, error)
type SubmitFunc func(context.Context, jobdb.SubmitJob) (jobdb.JobKey, error)

// Access checks every use before consulting a resolver, including cached ones.
// Callbacks belong to the controller and are never serialized into task input.
type Access struct {
	mu      sync.RWMutex
	ctx     context.Context
	grant   JobDB
	resolve ResolveFunc
	submit  SubmitFunc
}

func NewAccess(ctx context.Context, grant JobDB, resolve ResolveFunc, submit SubmitFunc) *Access {
	return &Access{ctx: ctx, grant: clone(grant), resolve: resolve, submit: submit}
}

// LimitDeadline narrows helper calls as soon as an operation-local timeout is
// known. Already issued credentials stay private to the trusted helper; later
// calls use a credential bounded by the narrower deadline.
func (a *Access) LimitDeadline(deadline time.Time) context.CancelFunc {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithDeadline(a.ctx, deadline)
	a.ctx = ctx
	return cancel
}
func (a *Access) Context() context.Context {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ctx
}
func (a *Access) Grant() JobDB { return clone(a.grant) }
func (a *Access) CheckSecrets(names []string) error {
	if err := a.Context().Err(); err != nil {
		return err
	}
	for _, name := range names {
		if !slices.Contains(a.grant.Secrets.Resolve, name) {
			return fmt.Errorf("secret %q: %w", name, jobdb.ErrLeasePermissionDenied)
		}
	}
	return nil
}
func (a *Access) Resolve(ctx context.Context, names []string) (jobdb.SecretResolveResult, error) {
	if err := ctx.Err(); err != nil {
		return jobdb.SecretResolveResult{}, err
	}
	if err := a.CheckSecrets(names); err != nil {
		return jobdb.SecretResolveResult{}, err
	}
	if len(names) == 0 {
		return jobdb.SecretResolveResult{}, nil
	}
	if a.resolve == nil {
		return jobdb.SecretResolveResult{}, fmt.Errorf("restricted secret resolver unavailable")
	}
	call, cancel := bound(ctx, a.Context())
	defer cancel()
	values, err := a.resolve(call, names)
	if err != nil {
		return jobdb.SecretResolveResult{}, err
	}
	if err := call.Err(); err != nil {
		return jobdb.SecretResolveResult{}, err
	}
	// Never expose unrelated entries even if a provider returned a wider cache.
	filtered := make(map[string]jobdb.SecretValue, len(names))
	for _, name := range names {
		if value, ok := values.Secrets[name]; ok {
			filtered[name] = value
		}
	}
	values.Secrets = filtered
	return values, nil
}
func (a *Access) Submit(ctx context.Context, job jobdb.SubmitJob) (jobdb.JobKey, error) {
	if err := a.Context().Err(); err != nil {
		return jobdb.JobKey{}, err
	}
	if !a.grant.Jobs.Submit {
		return jobdb.JobKey{}, jobdb.ErrLeasePermissionDenied
	}
	if a.submit == nil {
		return jobdb.JobKey{}, fmt.Errorf("restricted submission unavailable")
	}
	call, cancel := bound(ctx, a.Context())
	defer cancel()
	return a.submit(call, job)
}
func bound(caller, scope context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(scope, cancel)
	if d, ok := scope.Deadline(); ok {
		var c context.CancelFunc
		ctx, c = context.WithDeadline(ctx, d)
		old := cancel
		cancel = func() { c(); old() }
	}
	return ctx, func() { stop(); cancel() }
}

type accessKey struct{}

func WithAccess(ctx context.Context, access *Access) context.Context {
	return context.WithValue(ctx, accessKey{}, access)
}
func FromContext(ctx context.Context) *Access { a, _ := ctx.Value(accessKey{}).(*Access); return a }
