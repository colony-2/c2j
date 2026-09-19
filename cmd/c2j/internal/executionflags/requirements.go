package executionflags

import (
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/spf13/pflag"
)

// AddRequirementFlags binds job overrides, never actual allocation facts.
func AddRequirementFlags(flags *pflag.FlagSet, r *execution.Requirements) {
	for _, b := range []struct {
		name   string
		target **string
	}{
		{"cpu", &r.Resources.CPU}, {"memory", &r.Resources.Memory},
		{"ephemeral-storage", &r.Resources.EphemeralStorage}, {"platform", &r.Platform}, {"image", &r.Image},
	} {
		flags.Var(optionalString{b.target}, "require-"+b.name, "Job execution requirement override (not actual allocation)")
	}
}
