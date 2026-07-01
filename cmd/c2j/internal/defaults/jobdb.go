package defaults

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	configpkg "github.com/colony-2/c2j/pkg/config"
)

type JobDBTarget struct {
	URI        string
	RuntimeURL string
	TenantID   string
	Embedded   bool
}

func ResolveJobDBTarget(ctx context.Context, workingDir string, explicit string) (JobDBTarget, error) {
	raw := strings.TrimSpace(explicit)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(JobDBEnv))
	}
	if raw == "" {
		cfg, err := loadProjectConfig(workingDir)
		if err != nil {
			return JobDBTarget{}, err
		}
		if cfg != nil {
			raw, err = cfg.JobDBURI(ctx)
			if err != nil {
				return JobDBTarget{}, err
			}
		}
	}
	if strings.TrimSpace(raw) == "" {
		return JobDBTarget{}, nil
	}
	return ParseJobDBURI(raw)
}

func ParseJobDBURI(raw string) (JobDBTarget, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return JobDBTarget{}, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return JobDBTarget{}, fmt.Errorf("parse JobDB URI: %w", err)
	}
	switch parsed.Scheme {
	case "embed":
		return parseEmbeddedJobDBURI(parsed, raw)
	case "http", "https":
		return parseRemoteJobDBURI(parsed, raw)
	default:
		return JobDBTarget{}, fmt.Errorf("unsupported JobDB URI scheme %q", parsed.Scheme)
	}
}

func IsEmbeddedJobDBURI(raw string) bool {
	target, err := ParseJobDBURI(raw)
	return err == nil && target.Embedded
}

func parseEmbeddedJobDBURI(parsed *url.URL, raw string) (JobDBTarget, error) {
	if parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return JobDBTarget{}, fmt.Errorf("unsupported embedded JobDB URI %q: only %s is supported", raw, EmbedURL)
	}
	return JobDBTarget{
		URI:        EmbedURL,
		RuntimeURL: EmbedURL,
		TenantID:   EmbeddedTenantID,
		Embedded:   true,
	}, nil
}

func parseRemoteJobDBURI(parsed *url.URL, raw string) (JobDBTarget, error) {
	if parsed.Host == "" {
		return JobDBTarget{}, fmt.Errorf("remote JobDB URI %q requires a host", raw)
	}
	if parsed.User != nil {
		return JobDBTarget{}, fmt.Errorf("remote JobDB URI %q must not include user info", raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return JobDBTarget{}, fmt.Errorf("remote JobDB URI %q must not include query or fragment", raw)
	}

	escapedPath := parsed.EscapedPath()
	tenantPath := strings.TrimPrefix(escapedPath, "/")
	if tenantPath == "" {
		return JobDBTarget{}, fmt.Errorf("remote JobDB URI %q requires tenant path /<tenant-id>", raw)
	}
	if strings.Contains(tenantPath, "/") {
		return JobDBTarget{}, fmt.Errorf("remote JobDB URI %q must use exactly one tenant path segment", raw)
	}
	tenantID, err := url.PathUnescape(tenantPath)
	if err != nil {
		return JobDBTarget{}, fmt.Errorf("decode JobDB tenant path: %w", err)
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return JobDBTarget{}, fmt.Errorf("remote JobDB URI %q requires a non-empty tenant ID", raw)
	}
	if strings.Contains(tenantID, "/") {
		return JobDBTarget{}, fmt.Errorf("remote JobDB URI %q must use exactly one tenant path segment", raw)
	}

	runtimeURL := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
	return JobDBTarget{
		URI:        (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/" + tenantID}).String(),
		RuntimeURL: runtimeURL,
		TenantID:   tenantID,
	}, nil
}

func loadProjectConfig(workingDir string) (*configpkg.ProjectConfig, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workingDir = cwd
	}

	cfg, err := configpkg.LoadProjectConfig(workingDir)
	if err != nil {
		if errors.Is(err, configpkg.ErrConfigNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return cfg, nil
}
