package compiler

import (
	"fmt"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func artifactKeyIdentity(key jobdb.ArtifactKey) string {
	return fmt.Sprintf("%s:%d:%s", key.JobId, key.TaskOrdinal, key.Name)
}

func appendArtifactKeys(existing []jobdb.ArtifactKey, bindings map[string]recipeartifacts.Ref) []jobdb.ArtifactKey {
	if len(bindings) == 0 {
		return existing
	}
	seen := make(map[string]jobdb.ArtifactKey, len(existing)+len(bindings))
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
	out := make([]jobdb.ArtifactKey, 0, len(seen))
	for _, key := range seen {
		out = append(out, key)
	}
	return out
}
