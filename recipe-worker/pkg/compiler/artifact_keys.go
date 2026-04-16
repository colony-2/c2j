package compiler

import (
	"fmt"

	recipeartifacts "github.com/colony-2/c2j/recipe-core/pkg/artifacts"
	"github.com/colony-2/swf-go/pkg/swf"
)

func artifactKeyIdentity(key swf.ArtifactKey) string {
	return fmt.Sprintf("%s:%d:%s", key.JobId, key.TaskOrdinal, key.Name)
}

func appendArtifactKeys(existing []swf.ArtifactKey, bindings map[string]recipeartifacts.Ref) []swf.ArtifactKey {
	if len(bindings) == 0 {
		return existing
	}
	seen := make(map[string]swf.ArtifactKey, len(existing)+len(bindings))
	for _, key := range existing {
		seen[artifactKeyIdentity(key)] = key
	}
	for _, artifactRef := range bindings {
		key, ok := artifactRef.StoredKey()
		if !ok {
			continue
		}
		seen[artifactKeyIdentity(key)] = key
	}
	out := make([]swf.ArtifactKey, 0, len(seen))
	for _, key := range seen {
		out = append(out, key)
	}
	return out
}
