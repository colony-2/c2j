package workjob

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
)

func TestOptionsCompleteUsesProjectJobDB(t *testing.T) {
	t.Setenv(defaults.JobDBEnv, "")

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("jobdb: https://jobdb.example.invalid/configured\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	opts := Options{WorkingDir: root}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != "https://jobdb.example.invalid" || opts.TenantID != "configured" {
		t.Fatalf("resolved target = swf %q tenant %q", opts.SWFURL, opts.TenantID)
	}
	if opts.Concurrency != defaultConcurrency {
		t.Fatalf("Concurrency = %d, want %d", opts.Concurrency, defaultConcurrency)
	}
	if opts.AwaitThreshold != defaultAwaitThreshold {
		t.Fatalf("AwaitThreshold = %s, want %s", opts.AwaitThreshold, defaultAwaitThreshold)
	}
	if opts.Stdout == nil || opts.Stderr == nil {
		t.Fatal("stdio writers should be defaulted")
	}
	if !filepath.IsAbs(opts.WorkingDir) {
		t.Fatalf("WorkingDir = %q, want absolute path", opts.WorkingDir)
	}
}

func TestOptionsCompleteUsesJobDBEnvironment(t *testing.T) {
	t.Setenv(defaults.JobDBEnv, "https://jobdb.example.invalid/env-tenant")

	opts := Options{WorkingDir: t.TempDir()}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != "https://jobdb.example.invalid" || opts.TenantID != "env-tenant" {
		t.Fatalf("resolved target = swf %q tenant %q", opts.SWFURL, opts.TenantID)
	}
}

func TestRunOneOptionsCompleteUsesTenantZeroInEmbeddedMode(t *testing.T) {
	opts := RunOneOptions{WorkingDir: t.TempDir(), JobDBURI: defaults.EmbedURL}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != defaults.EmbedURL || opts.TenantID != defaults.EmbeddedTenantID {
		t.Fatalf("resolved target = swf %q tenant %q", opts.SWFURL, opts.TenantID)
	}
}

func TestReadyOptionsCompleteLeavesJobDBEmptyWhenUnknown(t *testing.T) {
	t.Setenv(defaults.JobDBEnv, "")

	opts := ReadyOptions{WorkingDir: t.TempDir()}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.TenantID != "" || opts.SWFURL != "" {
		t.Fatalf("resolved target = swf %q tenant %q, want empty", opts.SWFURL, opts.TenantID)
	}
}

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr string
	}{
		{
			name: "valid http",
			opts: Options{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047", Concurrency: 1},
		},
		{
			name: "valid https",
			opts: Options{JobDBURI: "https://swf.example.invalid/tenant", TenantID: "tenant", SWFURL: "https://swf.example.invalid", Concurrency: 2, AwaitThreshold: time.Second},
		},
		{
			name:    "missing jobdb",
			opts:    Options{Concurrency: 1},
			wantErr: "--jobdb is required",
		},
		{
			name:    "reject embed",
			opts:    Options{JobDBURI: defaults.EmbedURL, TenantID: defaults.EmbeddedTenantID, SWFURL: defaults.EmbedURL, Concurrency: 1},
			wantErr: "embed:/// is not supported",
		},
		{
			name:    "reject unsupported scheme",
			opts:    Options{JobDBURI: "ftp://example.invalid/tenant", TenantID: "tenant", SWFURL: "ftp://example.invalid", Concurrency: 1},
			wantErr: "remote JobDB URI",
		},
		{
			name:    "reject zero concurrency",
			opts:    Options{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047"},
			wantErr: "--concurrency must be > 0",
		},
		{
			name:    "reject negative concurrency",
			opts:    Options{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047", Concurrency: -1},
			wantErr: "--concurrency must be > 0",
		},
		{
			name:    "reject negative await threshold",
			opts:    Options{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047", Concurrency: 1, AwaitThreshold: -time.Second},
			wantErr: "--await-threshold must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(): %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestRunReturnsInvalidOptionExitCode(t *testing.T) {
	err := Run(context.Background(), Options{
		JobDBURI:    defaults.EmbedURL,
		Concurrency: 1,
	})
	if err == nil {
		t.Fatal("Run() succeeded, want validation error")
	}
	coded, ok := err.(interface{ ExitCode() int })
	if !ok {
		t.Fatalf("Run() error %T does not expose ExitCode", err)
	}
	if coded.ExitCode() != exitCodeInvalidOptions {
		t.Fatalf("ExitCode = %d, want %d", coded.ExitCode(), exitCodeInvalidOptions)
	}
}

func TestRunRejectsEmbeddedJobDBFromEnvironment(t *testing.T) {
	t.Setenv(defaults.JobDBEnv, defaults.EmbedURL)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		WorkingDir: t.TempDir(),
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if err == nil {
		t.Fatal("Run() succeeded, want embedded JobDB validation error")
	}
	coded, ok := err.(interface{ ExitCode() int })
	if !ok {
		t.Fatalf("Run() error %T does not expose ExitCode", err)
	}
	if coded.ExitCode() != exitCodeInvalidOptions {
		t.Fatalf("ExitCode = %d, want %d", coded.ExitCode(), exitCodeInvalidOptions)
	}
	if !strings.Contains(err.Error(), "embed:/// is not supported") {
		t.Fatalf("Run() error = %q, want embed rejection", err.Error())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no startup output", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no direct stderr output", stderr.String())
	}
}

func TestReadyOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    ReadyOptions
		wantErr string
	}{
		{
			name: "valid http",
			opts: ReadyOptions{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047"},
		},
		{
			name: "valid embed",
			opts: ReadyOptions{JobDBURI: defaults.EmbedURL, TenantID: defaults.EmbeddedTenantID, SWFURL: defaults.EmbedURL},
		},
		{
			name:    "missing jobdb",
			opts:    ReadyOptions{},
			wantErr: "--jobdb is required",
		},
		{
			name:    "unsupported scheme",
			opts:    ReadyOptions{JobDBURI: "ftp://example.invalid/tenant", TenantID: "tenant", SWFURL: "ftp://example.invalid"},
			wantErr: "unsupported JobDB runtime URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(): %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestRunOneOptionsValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    RunOneOptions
		wantErr string
	}{
		{
			name: "valid",
			opts: RunOneOptions{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047", LeaseDuration: time.Minute},
		},
		{
			name:    "missing jobdb",
			opts:    RunOneOptions{LeaseDuration: time.Minute},
			wantErr: "--jobdb is required",
		},
		{
			name:    "reject zero lease duration",
			opts:    RunOneOptions{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047"},
			wantErr: "--lease-duration must be > 0",
		},
		{
			name:    "reject negative await threshold",
			opts:    RunOneOptions{JobDBURI: "http://localhost:9047/tenant", TenantID: "tenant", SWFURL: "http://localhost:9047", LeaseDuration: time.Minute, AwaitThreshold: -time.Second},
			wantErr: "--await-threshold must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate(): %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() succeeded, want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
