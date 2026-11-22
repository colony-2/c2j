package extensions

import (
	"net/url"
	"strings"
	"unicode"
)

// DisplaySelector renders requested identity without resolving it or exposing URL
// credentials. Unrecognized or ambiguous syntax is redacted, never echoed.
func DisplaySelector(selector string) string {
	const redacted = "[redacted]"
	s := strings.TrimSpace(selector)
	if s == "" || strings.ContainsFunc(s, unicode.IsControl) {
		return redacted
	}
	if IsLocalSelector(s) {
		return s
	}
	if isGitOpSelector(s) {
		parsed, err := parseGitOpSelector(s)
		if err != nil || strings.ContainsAny(parsed.OpPath+parsed.Ref, "?#") {
			return redacted
		}
		repo, ok := displayRepository(parsed.RepositoryURL)
		if !ok {
			return redacted
		}
		parsed.RepositoryURL = repo
		return parsed.WithRef(parsed.Ref)
	}
	if IsNixSelector(s) {
		repo, attr, found := strings.Cut(strings.TrimPrefix(s, "nix:"), "#")
		if !found || attr == "" || strings.ContainsAny(attr, "?#@&=") {
			return redacted
		}
		if strings.Contains(repo, "://") {
			prefix := ""
			if strings.HasPrefix(repo, "git+") {
				prefix, repo = "git+", strings.TrimPrefix(repo, "git+")
			}
			safe, ok := displayRepository(repo)
			if !ok {
				return redacted
			}
			return "nix:" + prefix + safe + "#" + attr
		}
		// Canonical flake references and local paths carry no URL user information.
		if strings.ContainsAny(repo, "@?&#=") {
			return redacted
		}
		if !(repo == "nixpkgs" || strings.HasPrefix(repo, "github:") || strings.HasPrefix(repo, "gitlab:") || strings.HasPrefix(repo, "path:") || strings.HasPrefix(repo, "./") || strings.HasPrefix(repo, "../") || strings.HasPrefix(repo, "/")) {
			return redacted
		}
		return "nix:" + repo + "#" + attr
	}
	return redacted
}

func displayRepository(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return "", false
	}
	switch u.Scheme {
	case "http", "https", "ssh", "file":
	default:
		return "", false
	}
	if u.Scheme != "file" && u.Host == "" {
		return "", false
	}
	u.User = nil
	// Query parameters and URL fragments are not needed to identify a repository.
	// Drop the whole query so unknown credential names cannot slip through.
	u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = "", "", "", false
	return u.String(), true
}
