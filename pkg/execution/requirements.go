// Package execution defines portable demands and actual executor allocations.
// It has no scheduler dependencies and never treats a request as an allocation.
package execution

import "fmt"

const SchemaVersion = 2

// Requirements is also the partial override format: nil preserves the base
// property. Capabilities are additive; an empty list adds nothing. Scalar
// empty values are invalid; there is no explicit clearing operation.
type Requirements struct {
	// CapabilitiesDeclared preserves lexical list presence, including an empty list.
	// Overlay still accumulates capabilities; repository fallback alone uses this bit.
	CapabilitiesDeclared bool     `json:"-" yaml:"-"`
	Base                 *string  `json:"base,omitempty" yaml:"base,omitempty"`
	Capabilities         []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Python               *Python  `json:"python,omitempty" yaml:"python,omitempty"`
	Packages             []string `json:"packages,omitempty" yaml:"packages,omitempty"`
	// Deprecated: image and platform are retained for legacy records. Use Base.
	Image     *string   `json:"image,omitempty" yaml:"image,omitempty"`
	Platform  *string   `json:"platform,omitempty" yaml:"platform,omitempty"`
	Resources Resources `json:"resources,omitempty" yaml:"resources,omitempty"`
}

type Resources struct {
	CPU              *string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory           *string `json:"memory,omitempty" yaml:"memory,omitempty"`
	EphemeralStorage *string `json:"ephemeral-storage,omitempty" yaml:"ephemeral-storage,omitempty"`
}

type Image struct {
	Reference      string `json:"reference,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	ImageID        string `json:"image_id,omitempty"`
}

// Allocation contains simultaneous usable capacities reported by the trusted
// provisioner, not requested limits or capacities discovered from the host.
type Allocation struct {
	Base          string    `json:"base,omitempty"`
	Capabilities  []string  `json:"capabilities,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	Image         Image     `json:"image,omitzero"`
	Platform      *string   `json:"platform,omitempty"`
	Resources     Resources `json:"resources,omitempty"`
}

// Overlay returns an independent copy; callers cannot mutate either input via
// the returned pointers. Validate patches before overlaying them.
func Overlay(base, override Requirements) Requirements {
	return Requirements{
		CapabilitiesDeclared: base.CapabilitiesDeclared || override.CapabilitiesDeclared,
		Packages:             mergePackages(base.Packages, override.Packages),
		Python:               ChoosePython(base.Python, override.Python),
		Capabilities:         mergeCapabilities(base.Capabilities, override.Capabilities),
		Base:                 choose(base.Base, override.Base),
		Image:                choose(base.Image, override.Image),
		Platform:             choose(base.Platform, override.Platform),
		Resources: Resources{
			CPU:              choose(base.Resources.CPU, override.Resources.CPU),
			Memory:           choose(base.Resources.Memory, override.Resources.Memory),
			EphemeralStorage: choose(base.Resources.EphemeralStorage, override.Resources.EphemeralStorage),
		},
	}
}

func choose(base, override *string) *string {
	if override != nil {
		base = override
	}
	if base == nil {
		return nil
	}
	v := *base
	return &v
}

func (r Requirements) Empty() bool {
	return r.Python == nil && r.Base == nil && r.Image == nil && r.Platform == nil && r.Resources == (Resources{}) && len(r.Packages) == 0 && len(r.Capabilities) == 0
}

// Normalize validates all supplied fields and returns canonical values.
func (r Requirements) Normalize() (Requirements, error) {
	out := Overlay(r, Requirements{})
	if err := out.Python.Validate(); err != nil {
		return Requirements{}, err
	}
	if out.Base != nil {
		if out.Image != nil || out.Platform != nil {
			return Requirements{}, fmt.Errorf("execution.base cannot be combined with legacy image/platform requirements; replace all inherited image/platform constraints with base")
		}
		if err := validateBase(*out.Base, false); err != nil {
			return Requirements{}, fmt.Errorf("execution.base: %w", err)
		}
	}
	if err := normalizeLegacyRequirements(&out); err != nil {
		return Requirements{}, err
	}
	if err := validateCapabilities(out.Capabilities); err != nil {
		return Requirements{}, err
	}
	for _, ref := range out.Packages {
		if _, _, err := ParsePackage(ref); err != nil {
			return Requirements{}, err
		}
	}
	for _, field := range []struct {
		name      string
		value     **string
		normalize func(string) (string, error)
	}{
		{"resources.cpu", &out.Resources.CPU, normalizeCPU},
		{"resources.memory", &out.Resources.Memory, normalizeBytes},
		{"resources.ephemeral-storage", &out.Resources.EphemeralStorage, normalizeBytes},
	} {
		if *field.value == nil {
			continue
		}
		value, err := field.normalize(**field.value)
		if err != nil {
			return Requirements{}, fmt.Errorf("execution.%s: %w", field.name, err)
		}
		*field.value = &value
	}
	return out, nil
}

func (a Allocation) Normalize() (Allocation, error) {
	if a.SchemaVersion != SchemaVersion && a.SchemaVersion != legacyAllocationSchemaVersion {
		return Allocation{}, fmt.Errorf("unsupported allocation schema version %d", a.SchemaVersion)
	}
	if a.Base != "" {
		if a.SchemaVersion == legacyAllocationSchemaVersion {
			return Allocation{}, fmt.Errorf("allocation base requires schema version %d", SchemaVersion)
		}
		if err := validateBase(a.Base, true); err != nil {
			return Allocation{}, fmt.Errorf("execution base: %w", err)
		}
	}
	r, err := (Requirements{Resources: a.Resources, Capabilities: a.Capabilities}).Normalize()
	if err != nil {
		return Allocation{}, err
	}
	a.Resources, a.Capabilities = r.Resources, r.Capabilities
	return normalizeLegacyAllocation(a)
}

// HasCompatibilityFacts excludes image IDs, which are diagnostic only.
func (a Allocation) HasCompatibilityFacts() bool {
	return a.Base != "" || a.Image.Reference != "" || a.Image.ManifestDigest != "" || a.Platform != nil || a.Resources != (Resources{}) || len(a.Capabilities) > 0
}
