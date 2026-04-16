package test_fixtures_test

import (
	"testing"

	testfixtures "github.com/colony-2/c2j/recipe-worker/test-fixtures"
)

type echoInput struct {
	Message string `json:"message"`
}

type echoOutput struct {
	Output string `json:"output"`
}

func TestRecipeFixtures(t *testing.T) {
	_ = ensureFixtureOps()
	testfixtures.RunTestOnAllRecipes("recipes/*.test.yaml", t)
}
