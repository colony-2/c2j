package test_fixtures_test

import (
	"testing"

	"github.com/colony-2/c2j/pkg/ops"
	recipechild "github.com/colony-2/c2j/pkg/ops/recipe"
	testfixtures "github.com/colony-2/c2j/pkg/worker/test-fixtures"
)

func TestRecipeChildFixtures(t *testing.T) {
	ops.Register(recipechild.GetOps()...)
	testfixtures.RunTestOnAllRecipes("recipes/*.test.yaml", t)
}
