package preparedtest

import (
	"context"
	"time"
)

type clockKey struct{}

// WithClock supplies a case-local monotonic clock for live metric boundaries.
// It does not change diagnostic wall clocks or the shared process CPU sampler.
func WithClock(ctx context.Context, now func() time.Time) context.Context {
	return context.WithValue(ctx, clockKey{}, now)
}

func Clock(ctx context.Context) func() time.Time {
	if now, ok := ctx.Value(clockKey{}).(func() time.Time); ok {
		return now
	}
	return time.Now
}
