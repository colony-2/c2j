// Package executionflags is the shared argument/environment boundary for run
// allocation injection and opt-in list compatibility filters.
package executionflags

import (
	"fmt"

	"github.com/colony-2/c2j/pkg/execution"
	"github.com/spf13/pflag"
)

// Options preserves presence so an explicit empty argument cannot fall back to
// an environment value. Environment lookup happens at execution, not binding.
type Options struct {
	CPU              *string
	Memory           *string
	EphemeralStorage *string
	Platform         *string
	Image            *string
	ImageDigest      *string
	ImageID          *string
}

type binding struct {
	flag, env, help string
	target          **string
}

func (o *Options) bindings() []binding {
	return []binding{
		{"execution-cpu", "C2J_EXECUTION_CPU", "Actual usable CPU in cores or millicores", &o.CPU},
		{"execution-memory", "C2J_EXECUTION_MEMORY", "Actual usable memory with an SI/IEC unit", &o.Memory},
		{"execution-ephemeral-storage", "C2J_EXECUTION_EPHEMERAL_STORAGE", "Actual usable scratch capacity with an SI/IEC unit", &o.EphemeralStorage},
		{"execution-platform", "C2J_EXECUTION_PLATFORM", "Actual os/architecture[/variant]", &o.Platform},
		{"execution-image", "C2J_EXECUTION_IMAGE", "Launch image reference/tag", &o.Image},
		{"execution-image-digest", "C2J_EXECUTION_IMAGE_DIGEST", "Actual resolved OCI manifest digest", &o.ImageDigest},
		{"execution-image-id", "C2J_EXECUTION_IMAGE_ID", "Runtime image/config ID (diagnostic only)", &o.ImageID},
	}
}

func (o *Options) AddFlags(flags *pflag.FlagSet) {
	for _, b := range o.bindings() {
		flags.Var(optionalString{b.target}, b.flag, b.help+" (or "+b.env+")")
	}
}

// Parse resolves each argument independently ahead of its corresponding
// environment variable. No host detection, deployment defaults, or requested
// requirements are used as evidence of actual capacity. Pass os.LookupEnv in
// production, or nil to ignore the environment.
func (o Options) Parse(lookupEnv func(string) (string, bool)) (execution.Allocation, error) {
	for _, b := range o.bindings() {
		if *b.target != nil {
			if **b.target == "" {
				return execution.Allocation{}, fmt.Errorf("--%s must not be empty", b.flag)
			}
			continue
		}
		if lookupEnv != nil {
			if value, exists := lookupEnv(b.env); exists {
				if value == "" {
					return execution.Allocation{}, fmt.Errorf("%s must not be empty when set", b.env)
				}
				*b.target = &value
			}
		}
	}
	a := execution.Allocation{
		SchemaVersion: execution.SchemaVersion,
		Platform:      o.Platform,
		Resources:     execution.Resources{CPU: o.CPU, Memory: o.Memory, EphemeralStorage: o.EphemeralStorage},
	}
	if o.Image != nil {
		a.Image.Reference = *o.Image
	}
	if o.ImageDigest != nil {
		a.Image.ManifestDigest = *o.ImageDigest
	}
	if o.ImageID != nil {
		a.Image.ImageID = *o.ImageID
	}
	return a.Normalize()
}

// Filter is a candidate descriptor, not a lease request. An unresolved job can
// be included as a bootstrap candidate, but must never be labeled compatible.
type Filter struct {
	Allocation        execution.Allocation
	IncludeUnresolved bool
}

// ParseFilter does not inspect inherited allocation variables without explicit
// opt-in. In particular, listing children inside a worker must not silently
// hide children that require a different environment.
func (o Options) ParseFilter(enabled, includeUnresolved bool, lookupEnv func(string) (string, bool)) (*Filter, error) {
	if !enabled {
		if includeUnresolved {
			return nil, fmt.Errorf("--include-unresolved requires --compatible-with-execution")
		}
		for _, b := range o.bindings() {
			if *b.target != nil {
				return nil, fmt.Errorf("--%s requires --compatible-with-execution when listing jobs", b.flag)
			}
		}
		return nil, nil
	}
	allocation, err := o.Parse(lookupEnv)
	if err != nil {
		return nil, err
	}
	if !allocation.HasCompatibilityFacts() {
		return nil, fmt.Errorf("--compatible-with-execution requires at least one allocation fact; an image ID alone is diagnostic only")
	}
	return &Filter{Allocation: allocation, IncludeUnresolved: includeUnresolved}, nil
}

type optionalString struct{ target **string }

func (v optionalString) String() string {
	if *v.target == nil {
		return ""
	}
	return **v.target
}

func (v optionalString) Set(value string) error { *v.target = &value; return nil }
func (v optionalString) Type() string           { return "string" }
