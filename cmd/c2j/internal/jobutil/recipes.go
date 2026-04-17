package jobutil

import (
	"github.com/colony-2/c2j/pkg/worker/compiler"
)

func BuildRecipeSourceResolver() (compiler.RecipeSourceResolver, func(), error) {
	return compiler.NewRecipeSourceResolver(compiler.RecipeSourceResolverOptions{}), nil, nil
}
