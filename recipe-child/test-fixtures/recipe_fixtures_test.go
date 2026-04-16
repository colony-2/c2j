package test_fixtures_test

import (
	"testing"

	recipechild "github.com/colony-2/c2j/recipe-child/pkg/recipe"
	"github.com/colony-2/c2j/recipe-core/pkg/ops"
	testfixtures "github.com/colony-2/c2j/recipe-worker/test-fixtures"
)

func TestRecipeChildFixtures(t *testing.T) {
	ops.Register(recipechild.GetOps()...)
	testfixtures.RunTestOnAllRecipes("recipes/*.test.yaml", t)
}
