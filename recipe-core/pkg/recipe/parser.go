package recipe

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

func LoadRecipeFromString(data []byte) (*Recipe, error) {
	recipe := &Recipe{}
	return resolve(recipe, yaml.Unmarshal(data, recipe))
}

func LoadRecipeFromReader(r io.Reader) (*Recipe, error) {
	recipe := &Recipe{}
	d := yaml.NewDecoder(r)
	d.KnownFields(true)
	return resolve(recipe, d.Decode(&recipe))
}

func resolve(recipe *Recipe, err error) (*Recipe, error) {
	if err != nil {
		// Return decode errors as-is to preserve exact validation messages
		return nil, err
	}
	resolver := NewSharedNodeResolver(recipe.GetMetdata().Defs)
	walker := NewNodeWalker(resolver)
	result, err := walker.Walk(*recipe)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve shared nodes: %w", err)
	}

	return &result, err
}
