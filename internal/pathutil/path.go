// Package pathutil provides filesystem identity for existing objects.
package pathutil

import "path/filepath"

// CanonicalExisting returns the absolute, symlink-resolved name of an existing
// object. It creates nothing and propagates lookup errors. This is an identity
// at lookup time, not a confinement check or protection against later changes.
func CanonicalExisting(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}
