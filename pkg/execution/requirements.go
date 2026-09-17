// Package execution defines portable demands and actual executor allocations.
// It has no scheduler dependencies and never treats a request as an allocation.
package execution

import (
	_ "crypto/sha256" // Register OCI digest algorithms for go-digest validation.
	_ "crypto/sha512"
	"fmt"
	"regexp"
	"strings"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

const SchemaVersion = 1

// Requirements is also the partial override format: nil preserves the base
// property. Explicit empty values are invalid; version 1 does not support clear.
type Requirements struct {
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
	SchemaVersion int       `json:"schema_version"`
	Image         Image     `json:"image,omitempty"`
	Platform      *string   `json:"platform,omitempty"`
	Resources     Resources `json:"resources,omitempty"`
}

// Overlay returns an independent copy; callers cannot mutate either input via
// the returned pointers. Validate patches before overlaying them.
func Overlay(base, override Requirements) Requirements {
	return Requirements{
		Image:    choose(base.Image, override.Image),
		Platform: choose(base.Platform, override.Platform),
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
	return r.Image == nil && r.Platform == nil && r.Resources == (Resources{})
}

// Normalize validates all supplied fields and returns canonical values.
func (r Requirements) Normalize() (Requirements, error) {
	out := Overlay(r, Requirements{})
	for _, field := range []struct {
		name      string
		value     **string
		normalize func(string) (string, error)
	}{
		{"image", &out.Image, normalizeReference},
		{"platform", &out.Platform, normalizePlatform},
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
	if a.SchemaVersion != SchemaVersion {
		return Allocation{}, fmt.Errorf("unsupported allocation schema version %d", a.SchemaVersion)
	}
	r := Requirements{Platform: a.Platform, Resources: a.Resources}
	if a.Image.Reference != "" {
		r.Image = &a.Image.Reference
	}
	normalized, err := r.Normalize()
	if err != nil {
		return Allocation{}, err
	}
	a.Platform, a.Resources = normalized.Platform, normalized.Resources
	if normalized.Image != nil {
		a.Image.Reference = *normalized.Image
	}
	if a.Image.ManifestDigest != "" {
		if err := digest.Digest(a.Image.ManifestDigest).Validate(); err != nil {
			return Allocation{}, fmt.Errorf("execution image manifest digest: %w", err)
		}
	}
	if a.Image.ImageID != "" && strings.TrimSpace(a.Image.ImageID) != a.Image.ImageID {
		return Allocation{}, fmt.Errorf("execution image ID must not have surrounding whitespace")
	}
	// A pinned launch reference and a separately reported manifest must agree.
	if r.Image != nil && a.Image.ManifestDigest != "" {
		ref, _ := reference.ParseNormalizedNamed(a.Image.Reference)
		if pinned, ok := ref.(reference.Digested); ok && pinned.Digest().String() != a.Image.ManifestDigest {
			return Allocation{}, fmt.Errorf("execution image reference and manifest digest disagree")
		}
	}
	return a, nil
}

// HasCompatibilityFacts excludes image IDs, which are diagnostic only.
func (a Allocation) HasCompatibilityFacts() bool {
	return a.Image.Reference != "" || a.Image.ManifestDigest != "" || a.Platform != nil || a.Resources != (Resources{})
}

func normalizeReference(value string) (string, error) {
	ref, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return "", fmt.Errorf("invalid image reference %q: %w", value, err)
	}
	return reference.TagNameOnly(ref).String(), nil
}

var platformComponent = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)

func normalizePlatform(value string) (string, error) {
	parts := strings.Split(strings.ToLower(value), "/")
	if len(parts) < 2 || len(parts) > 3 {
		return "", fmt.Errorf("platform must be os/architecture[/variant]")
	}
	for _, part := range parts {
		if !platformComponent.MatchString(part) {
			return "", fmt.Errorf("invalid platform component %q", part)
		}
	}
	return strings.Join(parts, "/"), nil
}
