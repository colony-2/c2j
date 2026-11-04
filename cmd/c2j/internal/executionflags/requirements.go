package executionflags

import (
	"github.com/colony-2/c2j/pkg/execution"
	"github.com/spf13/pflag"
)

// AddRequirementFlags binds job overrides, never actual allocation facts.
func AddRequirementFlags(flags *pflag.FlagSet, r *execution.Requirements) {
	flags.StringArrayVar(&r.Capabilities, "require-capability", nil, "Required execution capability (repeatable; not actual allocation)")
	for _, b := range []struct {
		name   string
		target **string
	}{
		{"base", &r.Base},
		{"cpu", &r.Resources.CPU}, {"memory", &r.Resources.Memory},
		{"ephemeral-storage", &r.Resources.EphemeralStorage}, {"platform", &r.Platform}, {"image", &r.Image},
	} {
		flags.Var(optionalString{b.target}, "require-"+b.name, "Job execution requirement override (not actual allocation)")
	}
	_ = flags.MarkDeprecated("require-image", "use --require-base; image is only for legacy execution requirements")
	_ = flags.MarkDeprecated("require-platform", "use --require-base; platform is only for legacy execution requirements")
}
