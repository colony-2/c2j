package recipe

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// HashComputer computes canonical hashes for recipes
type HashComputer struct{}

// NewHashComputer creates a new hash computer
func NewHashComputer() *HashComputer {
	return &HashComputer{}
}

// ComputeRecipeHash computes a deterministic hash of recipe content
func (h *HashComputer) ComputeRecipeHash(recipe *Recipe) string {
	// Create a normalized representation

	// Convert to JSON for consistent serialization
	data, err := json.Marshal(recipe)
	if err != nil {
		// Fallback to a simple hash on error
		return fmt.Sprintf("%x", sha256.Sum256([]byte(recipe.GetMetdata().ID+recipe.GetMetdata().Version)))
	}

	// Compute SHA-256 hash
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}
