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
	// Rejected URIs may contain credentials, including in malformed URL text.
	// Describe the violated rule without echoing raw input or URL parser errors.
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Target{}, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("parse JobDB URI: invalid URL")
	}
	switch parsed.Scheme {
	case "embed":
		return parseEmbeddedJobDBURI(parsed)
	case "http", "https":
		return parseRemoteJobDBURI(parsed)
	default:
		return Target{}, fmt.Errorf("unsupported JobDB URI scheme: use http, https, or embed")
	}
}

func IsEmbeddedJobDBURI(raw string) bool {
	target, err := Parse(raw)
	return err == nil && target.Embedded
}

func parseEmbeddedJobDBURI(parsed *url.URL) (Target, error) {
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return Target{}, fmt.Errorf("unsupported embedded JobDB URI: only embed:/// is supported")
	}
	return Target{
		URI:        "embed:///",
		RuntimeURL: "embed:///",
		TenantID:   "0",
		Embedded:   true,
	}, nil
}

func parseRemoteJobDBURI(parsed *url.URL) (Target, error) {
	if parsed.Host == "" {
		return Target{}, fmt.Errorf("remote JobDB URI requires a host")
	}
	if parsed.User != nil {
		return Target{}, fmt.Errorf("remote JobDB URI must not include user info")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return Target{}, fmt.Errorf("remote JobDB URI must not include query or fragment")
	}

	escapedPath := parsed.EscapedPath()
	tenantPath := strings.TrimPrefix(escapedPath, "/")
	if tenantPath == "" {
		return Target{}, fmt.Errorf("remote JobDB URI requires tenant path /<tenant-id>")
	}
	if strings.Contains(tenantPath, "/") {
		return Target{}, fmt.Errorf("remote JobDB URI must use exactly one tenant path segment")
	}
	tenantID, err := url.PathUnescape(tenantPath)
	if err != nil {
		return Target{}, fmt.Errorf("decode JobDB tenant path: %w", err)
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return Target{}, fmt.Errorf("remote JobDB URI requires a non-empty tenant ID")
	}
	if strings.Contains(tenantID, "/") {
		return Target{}, fmt.Errorf("remote JobDB URI must use exactly one tenant path segment")
	}

	runtimeURL := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
	return Target{
		URI:        (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/" + tenantID}).String(),
		RuntimeURL: runtimeURL,
		TenantID:   tenantID,
	}, nil
}
