package test_fixtures_test

import (
	"testing"

	recipechild "github.com/colony-2/c2j/pkg/child/recipe"
	"github.com/colony-2/c2j/pkg/core/ops"
	testfixtures "github.com/colony-2/c2j/pkg/worker/test-fixtures"
)

func TestRecipeChildFixtures(t *testing.T) {
	ops.Register(recipechild.GetOps()...)
	testfixtures.RunTestOnAllRecipes("recipes/*.test.yaml", t)
}
