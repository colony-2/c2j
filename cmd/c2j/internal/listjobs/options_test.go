package listjobs

import (
	"context"
	"reflect"
	"testing"

	"github.com/colony-2/swf-go/pkg/swf"
)

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
