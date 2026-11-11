package dispatchtest

import "context"

type cleanupKey struct{}

// WithCleanupRetainer retains operation worktrees for a case-local final snapshot.
// The caller must run every registered cleanup after observing execution drain.
func WithCleanupRetainer(ctx context.Context, retain func(func())) context.Context {
	return context.WithValue(ctx, cleanupKey{}, retain)
}

func Cleanup(ctx context.Context, cleanup func()) {
	if retain, ok := ctx.Value(cleanupKey{}).(func(func())); ok {
		retain(cleanup)
		return
	}
	cleanup()
}
