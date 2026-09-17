package execution

import (
	"strings"

	"github.com/distribution/reference"
)

type Mismatch struct {
	Field     string `json:"field"`
	Required  string `json:"required"`
	Allocated string `json:"allocated,omitempty"`
	Reason    string `json:"reason"`
}

// Compare validates both operands. A nil error with no mismatches proves
// compatibility only with this supplied allocation, never lease readiness.
func Compare(required Requirements, allocated Allocation) ([]Mismatch, error) {
	r, err := required.Normalize()
	if err != nil {
		return nil, err
	}
	a, err := allocated.Normalize()
	if err != nil {
		return nil, err
	}
	var result []Mismatch
	check := func(field string, required *string, actual string, compatible bool) {
		if required == nil || compatible {
			return
		}
		reason := "incompatible"
		if actual == "" {
			reason = "unknown_allocation"
		}
		result = append(result, Mismatch{Field: field, Required: *required, Allocated: actual, Reason: reason})
	}
	if r.Image != nil {
		ref, _ := reference.ParseNormalizedNamed(*r.Image)
		if pinned, ok := ref.(reference.Digested); ok {
			check("image.manifest_digest", r.Image, a.Image.ManifestDigest, pinned.Digest().String() == a.Image.ManifestDigest)
		} else {
			check("image.reference", r.Image, a.Image.Reference, *r.Image == a.Image.Reference)
		}
	}
	if r.Platform != nil {
		actual := ""
		if a.Platform != nil {
			actual = *a.Platform
		}
		check("platform", r.Platform, actual, actual == *r.Platform || strings.HasPrefix(actual, *r.Platform+"/"))
	}
	for _, field := range []struct {
		name             string
		required, actual *string
		cpu              bool
	}{
		{"resources.cpu", r.Resources.CPU, a.Resources.CPU, true},
		{"resources.memory", r.Resources.Memory, a.Resources.Memory, false},
		{"resources.ephemeral-storage", r.Resources.EphemeralStorage, a.Resources.EphemeralStorage, false},
	} {
		if field.required == nil {
			continue
		}
		actual, compatible := "", false
		if field.actual != nil {
			actual = *field.actual
			_, minimum, _ := parseQuantity(*field.required, field.cpu)
			_, capacity, _ := parseQuantity(actual, field.cpu)
			compatible = capacity >= minimum
		}
		check(field.name, field.required, actual, compatible)
	}
	return result, nil
}
