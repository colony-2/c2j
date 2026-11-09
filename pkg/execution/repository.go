package execution

import (
	"context"
	"fmt"
)

// RepositoryFallback supplies only properties absent from lexical requirements.
// Strong submission and durable requirements must be overlaid afterwards.
func RepositoryFallback(defaults, scope Requirements) (Requirements, error) {
	if scope.CapabilitiesDeclared {
		defaults.Capabilities = nil
	}
	return Overlay(defaults, scope).Normalize()
}

type admissionKey struct{}
type RepositoryAdmission func(context.Context, Requirements) error

func WithRepositoryAdmission(ctx context.Context, f RepositoryAdmission) context.Context {
	return context.WithValue(ctx, admissionKey{}, f)
}
func AdmitRepository(ctx context.Context, defaults, scope Requirements) error {
	if f, ok := ctx.Value(admissionKey{}).(RepositoryAdmission); ok {
		return f(ctx, defaults)
	}
	effective, err := RepositoryFallback(defaults, scope)
	if err != nil {
		return err
	}
	effective.Packages = nil
	if effective.Base != nil && *effective.Base == "*" {
		effective.Base = nil
	}
	if !effective.Empty() {
		return fmt.Errorf("repository execution requirements require an execution-aware runtime and guarded task workers")
	}
	return nil
}
