package process

import "context"

type pythonEnvironmentKey struct{}

// WithPythonEnvironment protects interpreter activation from ambient and op
// variables. CLI executables with their own interpreter shebang remain isolated.
func WithPythonEnvironment(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, pythonEnvironmentKey{}, path)
}
