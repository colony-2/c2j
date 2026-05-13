package submitjob

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
)

func TestOptionsCompleteLeavesTenantEmptyWhenUnknown(t *testing.T) {
	t.Setenv(defaults.SWFEnv, "")
	t.Setenv(defaults.TenantEnv, "")

	opts := Options{WorkingDir: t.TempDir()}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != defaults.SWFURL {
		t.Fatalf("SWFURL = %q, want %q", opts.SWFURL, defaults.SWFURL)
	}
	if opts.TenantID != "" {
		t.Fatalf("TenantID = %q, want empty when no tenant can be derived", opts.TenantID)
	}
	if opts.Recipe != "default" {
		t.Fatalf("Recipe = %q, want default", opts.Recipe)
	}
}

func TestOptionsCompletePrefersEnvironmentOverHardcodedDefaults(t *testing.T) {
	t.Setenv(defaults.SWFEnv, "http://example.invalid:1234")
	t.Setenv(defaults.TenantEnv, "42")

	opts := Options{}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.SWFURL != "http://example.invalid:1234" {
		t.Fatalf("SWFURL = %q, want environment value", opts.SWFURL)
	}
	if opts.TenantID != "42" {
		t.Fatalf("TenantID = %q, want environment value", opts.TenantID)
	}
}

func TestOptionsCompleteUsesProjectTenantDefault(t *testing.T) {
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

	if opts.TenantID == "" {
		t.Fatalf("TenantID = %q, want project-derived tenant ID", opts.TenantID)
	}
}

func TestOptionsValidateAllowsDefaultSelfTarget(t *testing.T) {
	opts := Options{
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
