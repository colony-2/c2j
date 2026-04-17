package runjob

import (
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
)

func TestOptionsCompleteUsesHardcodedDefaults(t *testing.T) {
	t.Setenv(defaults.SWFEnv, "")
	t.Setenv(defaults.TenantEnv, "")

	opts := Options{}
	opts.Complete()

	if opts.SWFURL != defaults.SWFURL {
		t.Fatalf("SWFURL = %q, want %q", opts.SWFURL, defaults.SWFURL)
	}
	if opts.TenantID != defaults.TenantID {
		t.Fatalf("TenantID = %q, want %q", opts.TenantID, defaults.TenantID)
	}
}

func TestOptionsCompletePrefersEnvironmentOverHardcodedDefaults(t *testing.T) {
	t.Setenv(defaults.SWFEnv, "http://example.invalid:1234")
	t.Setenv(defaults.TenantEnv, "42")

	opts := Options{}
	opts.Complete()

	if opts.SWFURL != "http://example.invalid:1234" {
		t.Fatalf("SWFURL = %q, want environment value", opts.SWFURL)
	}
	if opts.TenantID != "42" {
		t.Fatalf("TenantID = %q, want environment value", opts.TenantID)
	}
}
