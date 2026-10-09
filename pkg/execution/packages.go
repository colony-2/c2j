package execution

import (
	"fmt"
	"strings"
)

// ParsePackage validates only local syntax. Resolution is deferred until use.
func ParsePackage(ref string) (manager, spec string, err error) {
	manager, spec, ok := strings.Cut(ref, ":")
	if !ok || spec == "" || strings.TrimSpace(ref) != ref || strings.ContainsAny(ref, "\x00\r\n") || strings.HasPrefix(spec, "-") || strings.Contains(spec, "${{") {
		return "", "", fmt.Errorf("invalid package reference %q", ref)
	}
	switch manager {
	case "nix", "pnpm", "uv":
		return manager, spec, nil
	default:
		return "", "", fmt.Errorf("unsupported package prefix %q (use nix, pnpm, or uv)", manager)
	}
}

func mergePackages(base, override []string) []string {
	var result []string
	seen := map[string]bool{}
	for _, list := range [][]string{base, override} {
		for _, ref := range list {
			if !seen[ref] {
				result = append(result, ref)
				seen[ref] = true
			}
		}
	}
	return result
}
