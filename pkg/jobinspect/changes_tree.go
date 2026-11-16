package jobinspect

import (
	"bytes"
	"encoding/hex"
)

// decodeChangesTree reads the stored bytes rather than ls-tree's normalized
// presentation. All names, modes and Git ordering must be valid before any
// children are followed or any commit/diff can be returned.
func decodeChangesTree(data []byte, hashBytes int) ([]changesTreeObject, []string, error) {
	if hashBytes != 20 && hashBytes != 32 {
		return nil, nil, changesErr("invalid_objects", "invalid tree object format")
	}
	var objects []changesTreeObject
	var children []string
	names := map[string]bool{}
	var previous []byte
	previousDir := false
	for len(data) > 0 {
		mode, rest, ok := bytes.Cut(data, []byte{' '})
		if !ok {
			return nil, nil, changesErr("invalid_objects", "malformed tree mode")
		}
		kind := "blob"
		directory, gitlink := false, false
		switch string(mode) {
		case "40000":
			kind, directory = "tree", true
		case "100644", "100755", "120000":
		case "160000":
			gitlink = true
		default:
			return nil, nil, changesErr("invalid_objects", "invalid tree entry mode")
		}
		name, rest, ok := bytes.Cut(rest, []byte{0})
		if !ok || len(rest) < hashBytes {
			return nil, nil, changesErr("invalid_objects", "truncated tree entry")
		}
		if len(name) == 0 || bytes.IndexByte(name, '/') >= 0 || bytes.Equal(name, []byte(".")) || bytes.Equal(name, []byte("..")) || bytes.EqualFold(name, []byte(".git")) {
			return nil, nil, changesErr("invalid_objects", "invalid tree entry name")
		}
		if names[string(name)] {
			return nil, nil, changesErr("invalid_objects", "duplicate tree entry name")
		}
		names[string(name)] = true
		if previous != nil && compareChangesTreeNames(previous, previousDir, name, directory) >= 0 {
			return nil, nil, changesErr("invalid_objects", "tree entries are not in Git order")
		}
		previous, previousDir = name, directory
		raw := rest[:hashBytes]
		nonzero := false
		for _, b := range raw {
			nonzero = nonzero || b != 0
		}
		if !nonzero {
			return nil, nil, changesErr("invalid_objects", "null tree entry object")
		}
		data = rest[hashBytes:]
		if gitlink {
			continue
		} // Opaque submodule target; existence is not required.
		hash := hex.EncodeToString(raw)
		objects = append(objects, changesTreeObject{hash, kind})
		if directory {
			children = append(children, hash)
		}
	}
	return objects, children, nil
}

// Git compares names bytewise with an implicit '/' for a directory and NUL for
// a file. Plain lexical sorting rejects valid prefix cases such as a.c then a/.
func compareChangesTreeNames(a []byte, aDir bool, b []byte, bDir bool) int {
	n := min(len(a), len(b))
	if c := bytes.Compare(a[:n], b[:n]); c != 0 {
		return c
	}
	var ac, bc byte
	if len(a) > n {
		ac = a[n]
	} else if aDir {
		ac = '/'
	}
	if len(b) > n {
		bc = b[n]
	} else if bDir {
		bc = '/'
	}
	return int(ac) - int(bc)
}
