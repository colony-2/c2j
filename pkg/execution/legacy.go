package execution

// Legacy image/platform support is isolated here for stored v3 demands, v1
// allocations, and durable directives. New placement uses Base exclusively.
import (
	_ "crypto/sha256"
	_ "crypto/sha512"
	"fmt"
	"regexp"
	"strings"

	"github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

const legacyDemandSchemaVersion = 3
const legacyAllocationSchemaVersion = 1

func normalizeLegacyRequirements(r *Requirements) error {
	for _, field := range []struct {
		name      string
		value     **string
		normalize func(string) (string, error)
	}{
		{"image", &r.Image, normalizeReference}, {"platform", &r.Platform, normalizePlatform},
	} {
		if *field.value == nil {
			continue
		}
		v, err := field.normalize(**field.value)
		if err != nil {
			return fmt.Errorf("execution.%s: %w", field.name, err)
		}
		*field.value = &v
	}
	return nil
}

func normalizeLegacyAllocation(a Allocation) (Allocation, error) {
	r := Requirements{Platform: a.Platform}
	if a.Image.Reference != "" {
		r.Image = &a.Image.Reference
	}
	if err := normalizeLegacyRequirements(&r); err != nil {
		return Allocation{}, err
	}
	a.Platform = r.Platform
	if r.Image != nil {
		a.Image.Reference = *r.Image
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

func compareLegacy(r Requirements, a Allocation, check func(string, *string, string, bool)) {
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
}
