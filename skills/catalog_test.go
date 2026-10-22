package skillsbundle_test

import (
	"strings"
	"testing"

	gitops "github.com/colony-2/c2j/pkg/git/export"
	"github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/c2j/pkg/ops/extensions"
	recipeops "github.com/colony-2/c2j/pkg/ops/recipe"
	workerops "github.com/colony-2/c2j/pkg/worker/export"
	skillsbundle "github.com/colony-2/c2j/skills"
)

// New public built-ins should not silently disappear from installed guidance.
func TestCatalogCoversExportedBuiltins(t *testing.T) {
	raw, err := skillsbundle.FS.ReadFile("c2j-recipes/references/ops-catalog.md")
	if err != nil {
		t.Fatal(err)
	}
	all := append([]ops.RegisterableOp{}, gitops.GetAll()...)
	all = append(all, workerops.GetAll()...)
	all = append(all, recipeops.GetOps()...)
	all = append(all, input.GetOp(), extensions.GetExecutionOp())
	for _, op := range all {
		name := op.GetMetadata().Type
		if !strings.Contains(string(raw), "`"+name+"`") {
			t.Errorf("public built-in %q is missing from the skill catalog", name)
		}
	}
}
