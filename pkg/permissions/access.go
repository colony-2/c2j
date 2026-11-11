package permissions

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/colony-2/c2j/internal/dispatchtest"
	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

type ResolveFunc func(context.Context, []string) (jobdb.SecretResolveResult, error)
type SubmitFunc func(context.Context, jobdb.SubmitJob) (jobref.Ref, error)
type CreateCellFunc func(context.Context, jobdb.CreateCellRequest) (jobdb.Cell, error)

// Access checks every use before consulting a resolver, including cached ones.
// Callbacks belong to the controller and are never serialized into task input.
type Access struct {
	mu         sync.RWMutex
	ctx        context.Context
	grant      JobDB
	resolve    ResolveFunc
	submit     SubmitFunc
	createCell CreateCellFunc
	restart    RestartFunc
}

func NewAccess(ctx context.Context, grant JobDB, resolve ResolveFunc, submit SubmitFunc, create ...CreateCellFunc) *Access {
	a := &Access{ctx: ctx, grant: clone(grant), resolve: resolve, submit: submit}
	if len(create) > 0 {
		a.createCell = create[0]
	}
	return a
}

// CreateCell registers a name without granting submission authority to it.
func (a *Access) CreateCell(ctx context.Context, req jobdb.CreateCellRequest) (result jobdb.Cell, err error) {
	finish, err := dispatchtest.Begin(a.Context(), dispatchtest.Attempt{Kind: "native", Name: "create_cell", Cell: req.Name})
	if err != nil {
		return result, err
	}
	defer func() { finish(err) }()
	if err := ctx.Err(); err != nil {
		return jobdb.Cell{}, err
	}
	if err := a.Context().Err(); err != nil {
		return jobdb.Cell{}, err
	}
	if !a.grant.Cells.Create || (a.grant.Cells.CellNames != nil && !slices.Contains(a.grant.Cells.CellNames, req.Name)) {
		return jobdb.Cell{}, jobdb.ErrLeasePermissionDenied
	}
	if a.createCell == nil {
		return jobdb.Cell{}, fmt.Errorf("restricted cell creation unavailable")
	}
	call, cancel := bound(ctx, a.Context())
	defer cancel()
	return a.createCell(call, req)
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
func (a *Access) Submit(ctx context.Context, job jobdb.SubmitJob) (result jobref.Ref, err error) {
	finish, err := dispatchtest.Begin(a.Context(), dispatchtest.Attempt{Kind: "native", Name: "submit_job", Cell: job.Cell})
	if err != nil {
		return result, err
	}
	defer func() { finish(err) }()
	// Lease delegation has no explicit primary-selection grant. Never inherit a
	// caller-supplied host hook as authority for a restricted task.
	if job.GoalID != "" {
		return jobref.Ref{}, jobdb.ErrPrimaryGoalSelectionForbidden
	}
	if err := a.Context().Err(); err != nil {
		return jobref.Ref{}, err
	}
	if !a.grant.Jobs.Submit || (a.grant.Jobs.CellNames != nil && !slices.Contains(a.grant.Jobs.CellNames, job.Cell)) {
		return jobref.Ref{}, jobdb.ErrLeasePermissionDenied
	}
	if a.submit == nil {
		return jobref.Ref{}, fmt.Errorf("restricted submission unavailable")
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

type inputResolverKey struct{}

// WithInputResolver installs a driver-only resolver. Operation helpers continue
// to use Access.Resolve and its delegated grant.
func WithInputResolver(ctx context.Context, resolve ResolveFunc) context.Context {
	return context.WithValue(ctx, inputResolverKey{}, resolve)
}

func ResolveInputs(ctx context.Context, names, authorized []string) (jobdb.SecretResolveResult, error) {
	access := FromContext(ctx)
	if access == nil {
		return jobdb.SecretResolveResult{}, fmt.Errorf("input secret resolver requires a task permission scope")
	}
	resolve, _ := ctx.Value(inputResolverKey{}).(ResolveFunc)
	if resolve == nil {
		return access.Resolve(ctx, names)
	}
	input := NewAccess(access.Context(), JobDB{Secrets: SecretPermissions{Resolve: authorized}}, resolve, nil)
	return input.Resolve(ctx, names)
}

func WithAccess(ctx context.Context, access *Access) context.Context {
	return context.WithValue(ctx, accessKey{}, access)
}
func FromContext(ctx context.Context) *Access { a, _ := ctx.Value(accessKey{}).(*Access); return a }

// RestartFunc is kept inside the controller, alongside ordinary submission.
type RestartFunc func(context.Context, jobdb.SubmitRestartJobRequest) (jobref.Ref, error)

// WithRestart installs the controller's lease-bound restart callback before the
// Access is published to an operation. It does not expand its submission grant.
func (a *Access) WithRestart(restart RestartFunc) *Access { a.restart = restart; return a }
func (a *Access) Restart(ctx context.Context, req jobdb.SubmitRestartJobRequest) (result jobref.Ref, err error) {
	finish, err := dispatchtest.Begin(a.Context(), dispatchtest.Attempt{Kind: "native", Name: "restart_job", Cell: req.Job.Cell})
	if err != nil {
		return result, err
	}
	defer func() { finish(err) }()
	if err := ctx.Err(); err != nil {
		return jobref.Ref{}, err
	}
	if err := a.Context().Err(); err != nil {
		return jobref.Ref{}, err
	}
	if !a.grant.Jobs.Submit || (a.grant.Jobs.CellNames != nil && !slices.Contains(a.grant.Jobs.CellNames, req.Job.Cell)) {
		return jobref.Ref{}, jobdb.ErrLeasePermissionDenied
	}
	if a.restart == nil {
		return jobref.Ref{}, fmt.Errorf("authorized restart submission capability unavailable")
	}
	call, cancel := bound(ctx, a.Context())
	defer cancel()
	return a.restart(call, req)
}
