package compiler

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/colony-2/c2j/pkg/core/recipe"
	"github.com/colony-2/c2j/pkg/core/starter"
	"github.com/colony-2/swf-go/pkg/swf"
	"gopkg.in/yaml.v3"
)

// Concrete implementation of the recipeRetriever
type recipeRetriever struct {
	// A map to quickly look up artifacts by name for initial loading
	artifactMap map[string]swf.Artifact

	// The cache for already-deserialized recipes
	recipeCache map[string]recipe.Recipe

	// Mutex to protect recipeCache from concurrent access
	mu sync.RWMutex
}

func (r *recipeRetriever) HasRecipe(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.artifactMap[name]
	return ok
}

func (r *recipeRetriever) GetRecipe(name string) (recipe.Recipe, error) {
	ctx := context.Background() // Or pass a context if appropriate

	// 1. Check Cache (Read Lock)
	r.mu.RLock()
	if rec, ok := r.recipeCache[name]; ok {
		r.mu.RUnlock()
		return rec, nil // Cache hit!
	}
	r.mu.RUnlock()

	// 2. Load and Store (Write Lock)
	r.mu.Lock()
	defer r.mu.Unlock()

	// Double check the cache after acquiring the write lock
	if rec, ok := r.recipeCache[name]; ok {
		return rec, nil
	}

	// Retrieve the artifact
	art, ok := r.artifactMap[name]
	if !ok {
		return recipe.Recipe{}, fmt.Errorf("recipe artifact not found: %s", name)
	}

	// Get bytes from the artifact

	yamlBytes, err := art.Bytes(ctx)
	if err != nil {
		return recipe.Recipe{}, fmt.Errorf("failed to read artifact bytes for %s: %w", name, err)
	}

	// Deserialize the recipe
	var newRecipe recipe.Recipe
	if err := yaml.Unmarshal(yamlBytes, &newRecipe); err != nil {
		return recipe.Recipe{}, fmt.Errorf("failed to unmarshal YAML for %s: %w. Data: %s", name, err, string(yamlBytes))
	}

	// Cache the deserialized recipe
	r.recipeCache[name] = newRecipe
	return newRecipe, nil
}

func newRetriever(artifacts []swf.Artifact) *recipeRetriever {
	artifactMap := make(map[string]swf.Artifact)

	for _, art := range artifacts {
		fullArtifactName := art.Name()

		// 1. Strip the constant suffix to get the pure recipe name
		recipeName := strings.TrimSuffix(fullArtifactName, starter.RecipeArtifactSuffix)

		if recipeName == fullArtifactName {
			// ignoring, not a recipe
			continue
		}

		// 2. Use the stripped recipe name as the key for O(1) lookup in GetRecipe
		artifactMap[recipeName] = art
	}

	return &recipeRetriever{
		artifactMap: artifactMap,
		recipeCache: make(map[string]recipe.Recipe),
	}
}
