package task

import "context"

// ActivityReporter observes live operations without changing recorded task data.
// The returned update accepts only a display-safe extension selector.
type ActivityReporter func(taskType string) (update func(string), finish func())
type activityKey struct{}

func WithActivityReporter(ctx context.Context, reporter ActivityReporter) context.Context {
	return context.WithValue(ctx, activityKey{}, reporter)
}

func BeginActivity(ctx context.Context, taskType string) (func(string), func()) {
	if report, ok := ctx.Value(activityKey{}).(ActivityReporter); ok && report != nil {
		return report(taskType)
	}
	return func(string) {}, func() {}
}
