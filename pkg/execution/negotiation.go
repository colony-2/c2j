package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const AllocationEnv = "C2J_EXECUTION_ALLOCATION"

// Environment describes this process, including the complete set of packages
// successfully prepared by c2j. Packages are not provider allocation claims.
type Environment struct {
	Allocation
	Packages []string `json:"packages"`
}

type environmentKey struct{}

// WithEnvironment supplies fresh attempt-local facts, never durable task input.
func WithEnvironment(ctx context.Context, allocation Allocation) context.Context {
	if allocation.SchemaVersion == 0 {
		allocation.SchemaVersion = SchemaVersion
	}
	return context.WithValue(ctx, environmentKey{}, Environment{Allocation: allocation, Packages: []string{}})
}

func CurrentEnvironment(ctx context.Context) Environment {
	v, ok := ctx.Value(environmentKey{}).(Environment)
	if !ok {
		return Environment{Allocation: Allocation{SchemaVersion: SchemaVersion}, Packages: []string{}}
	}
	v.Packages = append([]string{}, v.Packages...)
	return v
}

// WithPreparedPackages accumulates the encompassing package set, even when
// several preparation layers bind environments to the same invocation.
func WithPreparedPackages(ctx context.Context, packages []string) context.Context {
	v := CurrentEnvironment(ctx)
	v.Packages = append([]string{}, mergePackages(v.Packages, packages)...)
	return context.WithValue(ctx, environmentKey{}, v)
}

// Required is an internal control result. It must bypass output/Git acceptance
// and be translated to a durable yield by the execution runtime.
type Required struct {
	Needs Requirements
}

func (*Required) Error() string { return "extension requires another execution environment" }
func IsRequired(err error) bool { var r *Required; return errors.As(err, &r) }

// Negotiate adds constraints to the scheduling demand. The resumed task still
// has to meet its recipe's requirements; a request must not weaken those.
func Negotiate(d Demand, needs Requirements) (Demand, error) {
	effective, err := Overlay(Overlay(d.Effective, needs), d.Overrides).Normalize()
	if err != nil {
		return Demand{}, err
	}
	if needs.Base != nil && d.Overrides.Base != nil && *needs.Base != *d.Overrides.Base {
		base := *d.Overrides.Base
		if strings.Contains(base, "*") || !MatchBase(*needs.Base, base) {
			return Demand{}, fmt.Errorf("extension execution requirement conflicts with job-wide base override")
		}
	}
	m, err := Compare(Requirements{Resources: needs.Resources}, Allocation{SchemaVersion: SchemaVersion, Resources: effective.Resources})
	if err != nil {
		return Demand{}, err
	}
	if len(m) != 0 {
		return Demand{}, fmt.Errorf("extension execution requirement conflicts with job-wide resource override: %s", m[0].Field)
	}
	if current, requested := d.Effective.Base, effective.Base; current != nil && requested != nil {
		switch {
		case MatchBase(*current, *requested): // The request refines the enclosing selector.
		case MatchBase(*requested, *current):
			effective.Base = choose(nil, current)
		default:
			return Demand{}, fmt.Errorf("extension execution base cannot be combined with the enclosing base requirement")
		}
	}
	for _, field := range []struct {
		prior *string
		next  **string
		cpu   bool
	}{
		{d.Effective.Resources.CPU, &effective.Resources.CPU, true},
		{d.Effective.Resources.Memory, &effective.Resources.Memory, false},
		{d.Effective.Resources.EphemeralStorage, &effective.Resources.EphemeralStorage, false},
	} {
		if field.prior == nil {
			continue
		}
		_, prior, _ := parseQuantity(*field.prior, field.cpu)
		_, next, _ := parseQuantity(**field.next, field.cpu)
		if prior > next {
			*field.next = choose(nil, field.prior)
		}
	}
	d.Effective = effective
	return d, nil
}
