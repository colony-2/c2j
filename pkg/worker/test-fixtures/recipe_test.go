package testfixtures

import (
	"testing"
)

func TestAllRecipes(t *testing.T) {
	RunTestOnAllRecipes("recipes/*.test.yaml", t)
}
