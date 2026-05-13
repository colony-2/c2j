package listjobs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/colony-2/c2j/cmd/c2j/internal/defaults"
	"github.com/colony-2/swf-go/pkg/swf"
)

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

func TestOptionsCompleteLeavesTenantEmptyWhenUnknown(t *testing.T) {
	t.Setenv(defaults.SWFEnv, "")
	t.Setenv(defaults.TenantEnv, "")

	opts := Options{WorkingDir: t.TempDir()}
	if err := opts.Complete(context.Background()); err != nil {
		t.Fatalf("Complete(): %v", err)
	}

	if opts.TenantID != "" {
		t.Fatalf("TenantID = %q, want empty when no tenant can be derived", opts.TenantID)
	}
}

func TestBuildRequestDefaultsToCurrentCellAndVisibleStatuses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	req, err := buildRequest(context.Background(), Options{
		TenantID:   "tenant",
		WorkingDir: root,
	})
	if err != nil {
		t.Fatalf("buildRequest(): %v", err)
	}

	wantStatuses := defaultVisibleStatuses()
	if !reflect.DeepEqual(req.Statuses, wantStatuses) {
		t.Fatalf("Statuses = %#v, want %#v", req.Statuses, wantStatuses)
	}

	wantStores := []swf.JobStore{swf.JobStoreActive}
	if !reflect.DeepEqual(req.Stores, wantStores) {
		t.Fatalf("Stores = %#v, want %#v", req.Stores, wantStores)
	}

	predicates, err := swf.MetadataPredicates(req.MetadataFilter)
	if err != nil {
		t.Fatalf("MetadataPredicates(): %v", err)
	}
	if len(predicates) != 1 || len(predicates[0].Path) != 1 || predicates[0].Path[0] != "cell_name" || len(predicates[0].Values) != 1 || predicates[0].Values[0] != "alpha" {
		t.Fatalf("unexpected metadata predicates: %#v", predicates)
	}
}

func TestBuildRequestDefaultsToArchivedStoreForArchivedStatuses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	req, err := buildRequest(context.Background(), Options{
		TenantID:   "tenant",
		WorkingDir: root,
		Statuses:   []string{"completed,cancelled"},
	})
	if err != nil {
		t.Fatalf("buildRequest(): %v", err)
	}

	want := []swf.JobStore{swf.JobStoreArchived}
	if !reflect.DeepEqual(req.Stores, want) {
		t.Fatalf("Stores = %#v, want %#v", req.Stores, want)
	}
}

func TestBuildRequestUsesExplicitCellRepo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeListConfig(t, root, `
pattern: 'github.com/acme/boo-${{ cell }}'
self:
  repo: alpha
`)

	req, err := buildRequest(context.Background(), Options{
		TenantID:   "tenant",
		WorkingDir: root,
		Cell:       "github.com/acme/boo-beta",
	})
	if err != nil {
		t.Fatalf("buildRequest(): %v", err)
	}

	predicates, err := swf.MetadataPredicates(req.MetadataFilter)
	if err != nil {
		t.Fatalf("MetadataPredicates(): %v", err)
	}
	if len(predicates) != 1 || len(predicates[0].Values) != 1 || predicates[0].Values[0] != "beta" {
		t.Fatalf("unexpected metadata predicates: %#v", predicates)
	}
}

func TestParseWaitingForSupportsTaskTypesWithColons(t *testing.T) {
	t.Parallel()

	got, err := parseWaitingForFilters([]string{"recipe:input:collect_user_input"})
	if err != nil {
		t.Fatalf("parseWaitingForFilters(): %v", err)
	}

	want := []swf.JobTaskFilter{{
		JobType:  "recipe",
		TaskType: "input:collect_user_input",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseWaitingForFilters() = %#v, want %#v", got, want)
	}
}
