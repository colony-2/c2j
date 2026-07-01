package defaults

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestParseJobDBURIRemote(t *testing.T) {
	target, err := ParseJobDBURI("https://jobdb.example.com/acme-prod")
	if err != nil {
		t.Fatalf("ParseJobDBURI(): %v", err)
	}
	if target.RuntimeURL != "https://jobdb.example.com" {
		t.Fatalf("RuntimeURL = %q", target.RuntimeURL)
	}
	if target.TenantID != "acme-prod" {
		t.Fatalf("TenantID = %q", target.TenantID)
	}
	if target.Embedded {
		t.Fatal("Embedded = true, want false")
	}
}

func TestParseJobDBURIRejectsMissingOrNestedTenant(t *testing.T) {
	for _, raw := range []string{
		"https://jobdb.example.com",
		"https://jobdb.example.com/",
		"https://jobdb.example.com/tenant/",
		"https://jobdb.example.com/path/tenant",
		"https://jobdb.example.com/a%2Fb",
		"https://jobdb.example.com/tenant?x=1",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := ParseJobDBURI(raw); err == nil {
				t.Fatalf("ParseJobDBURI(%q) succeeded, want error", raw)
			}
		})
	}
}

func TestParseJobDBURIEmbedded(t *testing.T) {
	target, err := ParseJobDBURI(EmbedURL)
	if err != nil {
		t.Fatalf("ParseJobDBURI(): %v", err)
	}
	if target.RuntimeURL != EmbedURL {
		t.Fatalf("RuntimeURL = %q", target.RuntimeURL)
	}
	if target.TenantID != EmbeddedTenantID {
		t.Fatalf("TenantID = %q", target.TenantID)
	}
	if !target.Embedded {
		t.Fatal("Embedded = false, want true")
	}
}

func TestParseJobDBURIRejectsEmbeddedRoot(t *testing.T) {
	if _, err := ParseJobDBURI("embed:///tmp/c2j"); err == nil {
		t.Fatal("ParseJobDBURI(embed root) succeeded, want error")
	}
}

func TestResolveJobDBTargetUsesEnvironment(t *testing.T) {
	t.Setenv(JobDBEnv, "http://localhost:9047/dev")

	target, err := ResolveJobDBTarget(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatalf("ResolveJobDBTarget(): %v", err)
	}
	if target.RuntimeURL != "http://localhost:9047" || target.TenantID != "dev" {
		t.Fatalf("target = %#v", target)
	}
}

func TestResolveJobDBTargetUsesProjectConfig(t *testing.T) {
	t.Setenv(JobDBEnv, "")

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("jobdb: https://jobdb.example.com/acme-prod\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	target, err := ResolveJobDBTarget(context.Background(), root, "")
	if err != nil {
		t.Fatalf("ResolveJobDBTarget(): %v", err)
	}
	if target.RuntimeURL != "https://jobdb.example.com" || target.TenantID != "acme-prod" {
		t.Fatalf("target = %#v", target)
	}
}

func TestResolveJobDBTargetUsesEmbeddedProjectConfig(t *testing.T) {
	t.Setenv(JobDBEnv, "")

	root := t.TempDir()
	configPath := filepath.Join(root, ".c2j", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("jobdb: embed:///\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	target, err := ResolveJobDBTarget(context.Background(), root, "")
	if err != nil {
		t.Fatalf("ResolveJobDBTarget(): %v", err)
	}
	if !target.Embedded || target.RuntimeURL != EmbedURL || target.TenantID != EmbeddedTenantID {
		t.Fatalf("target = %#v", target)
	}
}
