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

func TestOptionsCompleteDefaults(t *testing.T) {
	t.Setenv(defaults.SWFEnv, "")
	t.Setenv(defaults.TenantEnv, "")

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("self:\n  repo: github.com/acme/self\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	opts := Options{WorkingDir: root}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != defaults.SWFURL {
		t.Fatalf("SWFURL = %q, want %q", opts.SWFURL, defaults.SWFURL)
	}
	if opts.TenantID == "" {
		t.Fatalf("TenantID = %q, want project-derived tenant ID", opts.TenantID)
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

func TestOptionsCompletePrefersEnvironment(t *testing.T) {
	t.Setenv(defaults.SWFEnv, "https://swf.example.invalid")
	t.Setenv(defaults.TenantEnv, "tenant-env")

	opts := Options{WorkingDir: t.TempDir()}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != "https://swf.example.invalid" {
		t.Fatalf("SWFURL = %q, want environment value", opts.SWFURL)
	}
	if opts.TenantID != "tenant-env" {
		t.Fatalf("TenantID = %q, want environment value", opts.TenantID)
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
			opts: Options{TenantID: "tenant", SWFURL: "http://localhost:9047", Concurrency: 1},
		},
		{
			name: "valid https",
			opts: Options{TenantID: "tenant", SWFURL: "https://swf.example.invalid", Concurrency: 2, AwaitThreshold: time.Second},
		},
		{
			name:    "missing tenant",
			opts:    Options{SWFURL: "http://localhost:9047", Concurrency: 1},
			wantErr: "--tenant-id is required",
		},
		{
			name:    "missing swf url",
			opts:    Options{TenantID: "tenant", Concurrency: 1},
			wantErr: "--swf-url is required",
		},
		{
			name:    "reject embed",
			opts:    Options{TenantID: "tenant", SWFURL: defaults.EmbedURL, Concurrency: 1},
			wantErr: "embed:/// is not supported",
		},
		{
			name:    "reject unsupported scheme",
			opts:    Options{TenantID: "tenant", SWFURL: "ftp://example.invalid", Concurrency: 1},
			wantErr: "external SWF runtime URL",
		},
		{
			name:    "reject zero concurrency",
			opts:    Options{TenantID: "tenant", SWFURL: "http://localhost:9047"},
			wantErr: "--concurrency must be > 0",
		},
		{
			name:    "reject negative concurrency",
			opts:    Options{TenantID: "tenant", SWFURL: "http://localhost:9047", Concurrency: -1},
			wantErr: "--concurrency must be > 0",
		},
		{
			name:    "reject negative await threshold",
			opts:    Options{TenantID: "tenant", SWFURL: "http://localhost:9047", Concurrency: 1, AwaitThreshold: -time.Second},
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
		TenantID:    "tenant",
		SWFURL:      defaults.EmbedURL,
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

func TestRunRejectsEmbeddedSWFURLFromEnvironment(t *testing.T) {
	t.Setenv(defaults.SWFEnv, defaults.EmbedURL)
	t.Setenv(defaults.TenantEnv, "tenant-env")

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), Options{
		WorkingDir: t.TempDir(),
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if err == nil {
		t.Fatal("Run() succeeded, want embedded SWF URL validation error")
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
