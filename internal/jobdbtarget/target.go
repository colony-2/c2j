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
	return parse(raw, false)
}

func ParseCatalog(raw string) (Target, error) {
	target, err := parse(raw, true)
	target.TenantID = ""
	return target, err
}

func parse(raw string, catalog bool) (Target, error) {
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
		if catalog && parsed.ForceQuery {
			return Target{}, fmt.Errorf("remote JobDB URI must not include query or fragment")
		}
		return parseRemoteJobDBURI(parsed, catalog)
	default:
		return Target{}, fmt.Errorf("unsupported JobDB URI scheme: use http, https, or embed")
	}
}

func IsEmbeddedJobDBURI(raw string) bool {
	target, err := Parse(raw)
	return err == nil && target.Embedded
}

func parseEmbeddedJobDBURI(parsed *url.URL) (Target, error) {
	if parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return Target{}, fmt.Errorf("unsupported embedded JobDB URI: use embed:/// or embed:///PROJECT_ID")
	}
	id, uri := "0", "embed:///"
	if parsed.Path != "" && parsed.Path != "/" {
		id = strings.TrimSpace(strings.TrimPrefix(parsed.Path, "/"))
		if !strings.HasPrefix(parsed.EscapedPath(), "/") || id == "" || strings.Contains(id, "/") {
			return Target{}, fmt.Errorf("embedded JobDB URI must use exactly one project ID segment")
		}
		uri = "embed:///" + url.PathEscape(id)
	}
	return Target{
		URI:        uri,
		RuntimeURL: "embed:///",
		TenantID:   id,
		Embedded:   true,
	}, nil
}

func parseRemoteJobDBURI(parsed *url.URL, catalog bool) (Target, error) {
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
	runtimeURL := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
	if tenantPath == "" {
		if catalog {
			return Target{URI: runtimeURL, RuntimeURL: runtimeURL}, nil
		}
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

	return Target{
		URI:        (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/" + tenantID}).String(),
		RuntimeURL: runtimeURL,
		TenantID:   tenantID,
	}, nil
}
