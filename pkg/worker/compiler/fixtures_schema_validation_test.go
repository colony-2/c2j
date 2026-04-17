package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/colony-2/c2j/pkg/worker/ops" // ensure test ops are registered via init

	recipe "github.com/colony-2/c2j/pkg/recipe"
)

// Test that fixture recipes conform to the generated schema and that
// required ops are present in the registry. This relies on ops init()
// in recipe-worker/pkg/ops/test_activities.go registering test ops.
func TestFixtureRecipes_ValidateSchema(t *testing.T) {
	fixturesDir := filepath.Join("..", "test-fixtures", "recipes")
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}

	expectedFail := map[string]bool{}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".test.yaml") {
			continue
		}
		// Skip known negative fixtures maintained for other test suites
		if strings.Contains(name, "missing") || strings.Contains(name, "invalid") {
			continue
		}
		wantErr := expectedFail[name]

		path := filepath.Join(fixturesDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}

		t.Run(name, func(t *testing.T) {
			err := recipe.Validate(string(data))
			if wantErr && err == nil {
				t.Fatalf("expected validation error for %s, got nil", name)
			}
			if !wantErr && err != nil {
				t.Fatalf("unexpected validation error for %s: %v", name, err)
			}
		})
	}
}
