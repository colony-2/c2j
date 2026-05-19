package workjob

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
)

const (
	defaultConcurrency    = 1
	defaultAwaitThreshold = 30 * time.Second
)

type Options struct {
	TenantID       string
	SWFURL         string
	Concurrency    int
	AwaitThreshold time.Duration
	WorkingDir     string
	Stdout         io.Writer
	Stderr         io.Writer
}

func (o *Options) Complete(ctx context.Context) error {
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = strings.TrimSpace(os.Getenv(defaults.SWFEnv))
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		o.SWFURL = defaults.SWFURL
	}
	if o.Concurrency == 0 {
		o.Concurrency = defaultConcurrency
	}
	if o.AwaitThreshold == 0 {
		o.AwaitThreshold = defaultAwaitThreshold
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if strings.TrimSpace(o.WorkingDir) == "" {
		if cwd, err := os.Getwd(); err == nil {
			o.WorkingDir = cwd
		}
	}
	if strings.TrimSpace(o.WorkingDir) != "" {
		if absPath, err := filepath.Abs(o.WorkingDir); err == nil {
			o.WorkingDir = absPath
		}
	}
	if strings.TrimSpace(o.TenantID) == "" {
		o.TenantID = strings.TrimSpace(os.Getenv(defaults.TenantEnv))
	}
	if strings.TrimSpace(o.TenantID) == "" {
		tenantID, err := defaults.ResolveTenantID(ctx, o.WorkingDir)
		if err != nil {
			return err
		}
		o.TenantID = tenantID
	}
	return nil
}

func (o Options) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("--tenant-id is required (or %s, or project self.tenant_id/self.repo)", defaults.TenantEnv)
	}
	if strings.TrimSpace(o.SWFURL) == "" {
		return fmt.Errorf("--swf-url is required (or %s)", defaults.SWFEnv)
	}
	if err := validateWorkSWFURL(o.SWFURL); err != nil {
		return err
	}
	if o.Concurrency <= 0 {
		return fmt.Errorf("--concurrency must be > 0")
	}
	if o.AwaitThreshold < 0 {
		return fmt.Errorf("--await-threshold must be >= 0")
	}
	return nil
}

func validateWorkSWFURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse --swf-url: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https":
		return nil
	case "embed":
		return fmt.Errorf("c2j work requires an external SWF runtime; %s is not supported", defaults.EmbedURL)
	default:
		return fmt.Errorf("c2j work requires an external SWF runtime URL (http(s)://...), got %q", raw)
	}
}

func validateSWFURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse --swf-url: %w", err)
	}
	switch parsed.Scheme {
	case "http", "https", "embed":
		return nil
	default:
		return fmt.Errorf("unsupported SWF runtime URL %q", raw)
	}
}

type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit %d", e.code)
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }
func (e exitError) ExitCode() int { return e.code }
