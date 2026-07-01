package submitjob

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
	if opts.Recipe != "default" {
		t.Fatalf("Recipe = %q, want default", opts.Recipe)
	}
}

func TestOptionsCompleteUsesJobDBEnvironment(t *testing.T) {
	t.Setenv(defaults.JobDBEnv, "https://jobdb.example.invalid/prod")

	opts := Options{}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != "https://jobdb.example.invalid" {
		t.Fatalf("SWFURL = %q", opts.SWFURL)
	}
	if opts.TenantID != "prod" {
		t.Fatalf("TenantID = %q", opts.TenantID)
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

	if opts.SWFURL != defaults.EmbedURL {
		t.Fatalf("SWFURL = %q", opts.SWFURL)
	}
	if opts.TenantID != defaults.EmbeddedTenantID {
		t.Fatalf("TenantID = %q, want embedded tenant ID %q", opts.TenantID, defaults.EmbeddedTenantID)
	}
}

func TestOptionsValidateRequiresJobDB(t *testing.T) {
	opts := Options{Recipe: "default"}
	if err := opts.Validate(); err == nil || !strings.Contains(err.Error(), "--jobdb is required") {
		t.Fatalf("Validate() = %v, want --jobdb required", err)
	}
}

func TestOptionsValidateAllowsDefaultSelfTarget(t *testing.T) {
	opts := Options{
		JobDBURI: "http://example.invalid/tenant",
		TenantID: "tenant",
		SWFURL:   "http://example.invalid",
		Recipe:   "default",
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("expected missing target to default to self, got %v", err)
	}

	opts.Self = true
	opts.Cell = "github.com/acme/other"
	if err := opts.Validate(); err == nil {
		t.Fatal("expected conflicting targets to fail validation")
	}
}

func TestOptionsValidateRejectsJSONOutputWithRun(t *testing.T) {
	opts := Options{
		JobDBURI:       "http://example.invalid/tenant",
		TenantID:       "tenant",
		SWFURL:         "http://example.invalid",
		Recipe:         "default",
		Self:           true,
		JSONOutput:     true,
		RunAfterSubmit: true,
	}

	if err := opts.Validate(); err == nil {
		t.Fatal("expected --json and --run to fail validation together")
	}
}
