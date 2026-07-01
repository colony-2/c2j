package runjob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
)

func TestOptionsCompleteUsesExplicitJobDBURI(t *testing.T) {
	opts := Options{JobDBURI: "http://localhost:9047/dev"}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != "http://localhost:9047" {
		t.Fatalf("SWFURL = %q", opts.SWFURL)
	}
	if opts.TenantID != "dev" {
		t.Fatalf("TenantID = %q", opts.TenantID)
	}
}

func TestOptionsCompleteUsesJobDBEnvironment(t *testing.T) {
	t.Setenv(defaults.JobDBEnv, "https://jobdb.example.invalid/prod")

	opts := Options{}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != "https://jobdb.example.invalid" || opts.TenantID != "prod" {
		t.Fatalf("resolved target = swf %q tenant %q", opts.SWFURL, opts.TenantID)
	}
}

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
}

func TestOptionsCompleteUsesTenantZeroInEmbeddedMode(t *testing.T) {
	opts := Options{JobDBURI: defaults.EmbedURL}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != defaults.EmbedURL || opts.TenantID != defaults.EmbeddedTenantID {
		t.Fatalf("resolved target = swf %q tenant %q", opts.SWFURL, opts.TenantID)
	}
}

func TestOptionsValidateRequiresJobDB(t *testing.T) {
	opts := Options{JobID: "job"}
	if err := opts.Validate(); err == nil || !strings.Contains(err.Error(), "--jobdb is required") {
		t.Fatalf("Validate() = %v, want --jobdb required", err)
	}
}
