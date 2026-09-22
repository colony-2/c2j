// Package jobdbtarget parses explicit CLI-compatible connection targets.
package jobdbtarget

import (
	"fmt"
	"net/url"
	"strings"
)

type Target struct {
	URI        string
	RuntimeURL string
	TenantID   string
	Embedded   bool
}

func Parse(raw string) (Target, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Target{}, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("parse JobDB URI: %w", err)
	}
	switch parsed.Scheme {
	case "embed":
		return parseEmbeddedJobDBURI(parsed, raw)
	case "http", "https":
		return parseRemoteJobDBURI(parsed, raw)
	default:
		return Target{}, fmt.Errorf("unsupported JobDB URI scheme %q", parsed.Scheme)
	}
}

func IsEmbeddedJobDBURI(raw string) bool {
	target, err := Parse(raw)
	return err == nil && target.Embedded
}

func parseEmbeddedJobDBURI(parsed *url.URL, raw string) (Target, error) {
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return Target{}, fmt.Errorf("unsupported embedded JobDB URI %q: only %s is supported", raw, "embed:///")
	}
	return Target{
		URI:        "embed:///",
		RuntimeURL: "embed:///",
		TenantID:   "0",
		Embedded:   true,
	}, nil
}

func parseRemoteJobDBURI(parsed *url.URL, raw string) (Target, error) {
	if parsed.Host == "" {
		return Target{}, fmt.Errorf("remote JobDB URI %q requires a host", raw)
	}
	if parsed.User != nil {
		return Target{}, fmt.Errorf("remote JobDB URI %q must not include user info", raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return Target{}, fmt.Errorf("remote JobDB URI %q must not include query or fragment", raw)
	}

	escapedPath := parsed.EscapedPath()
	tenantPath := strings.TrimPrefix(escapedPath, "/")
	if tenantPath == "" {
		return Target{}, fmt.Errorf("remote JobDB URI %q requires tenant path /<tenant-id>", raw)
	}
	if strings.Contains(tenantPath, "/") {
		return Target{}, fmt.Errorf("remote JobDB URI %q must use exactly one tenant path segment", raw)
	}
	tenantID, err := url.PathUnescape(tenantPath)
	if err != nil {
		return Target{}, fmt.Errorf("decode JobDB tenant path: %w", err)
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return Target{}, fmt.Errorf("remote JobDB URI %q requires a non-empty tenant ID", raw)
	}
	if strings.Contains(tenantID, "/") {
		return Target{}, fmt.Errorf("remote JobDB URI %q must use exactly one tenant path segment", raw)
	}

	runtimeURL := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
	return Target{
		URI:        (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/" + tenantID}).String(),
		RuntimeURL: runtimeURL,
		TenantID:   tenantID,
	}, nil
}
