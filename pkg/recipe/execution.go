package recipe

import (
	"crypto/sha256"
	"fmt"
	"gopkg.in/yaml.v3"
)

// ExecutionDigest identifies the serialized pinned recipe, including expanded
// inline boundaries. Round-tripping first makes programmatic and loaded recipes
// agree on omitted fields and canonical execution quantities.
func ExecutionDigest(r Recipe) (string, error) {
	raw, err := yaml.Marshal(&r)
	if err != nil {
		return "", err
	}
	var normalized any
	if err := yaml.Unmarshal(raw, &normalized); err != nil {
		return "", err
	}
	raw, err = yaml.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}
