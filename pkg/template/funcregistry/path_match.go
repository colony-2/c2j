package funcregistry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

func pathMatchCELFunc(adapter types.Adapter, _ ContextProvider) cel.EnvOption {
	return cel.Function("path_match", cel.Overload(
		"path_match_string_list_string",
		[]*cel.Type{cel.StringType, cel.ListType(cel.StringType)},
		cel.MapType(cel.StringType, cel.DynType),
		cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
			reference, ok := lhs.(types.String)
			if !ok {
				return types.NewErr("path_match: expected string reference")
			}
			result, err := pathMatchTemplate(string(reference), normalizeJQInput(rhs))
			if err != nil {
				return types.NewErr("%v", err)
			}
			return adapter.NativeToValue(result)
		}),
	))
}

// Template lists may be either typed slices or JSON-decoded lists. Check types
// before path validation so malformed paths remain data, but bad types do not.
func pathMatchTemplate(reference string, inventory any) (map[string]any, error) {
	var candidates []string
	switch list := inventory.(type) {
	case []string:
		candidates = list
	case []any:
		candidates = make([]string, len(list))
		for i, item := range list {
			value, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("path_match: candidate %d must be a string", i)
			}
			candidates[i] = value
		}
	default:
		return nil, fmt.Errorf("path_match: expected list<string> candidates")
	}
	return pathMatch(reference, candidates), nil
}

// pathMatch compares lexical names only; it never accesses the filesystem.
func pathMatch(reference string, candidates []string) map[string]any {
	result := map[string]any{"status": "invalid", "path": "", "candidates": []string{}}
	components, ok := pathMatchReference(reference)
	if !ok {
		return result
	}
	unique := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !pathMatchCandidateValid(candidate) {
			return result
		}
		unique[candidate] = struct{}{}
	}
	if _, exact := unique[reference]; exact {
		result["status"] = "matched"
		result["path"] = reference
		result["candidates"] = []string{reference}
		return result
	}
	for start := range components {
		suffix := strings.Join(components[start:], "/")
		matches := []string{}
		for candidate := range unique {
			if candidate == suffix || strings.HasSuffix(candidate, "/"+suffix) {
				matches = append(matches, candidate)
			}
		}
		if len(matches) == 0 {
			continue
		}
		sort.Strings(matches)
		result["candidates"] = matches
		result["status"] = "ambiguous"
		if len(matches) == 1 {
			result["status"] = "matched"
			result["path"] = matches[0]
		}
		return result
	}
	result["status"] = "not_found"
	return result
}

func pathMatchReference(reference string) ([]string, bool) {
	if reference == "" || strings.ContainsAny(reference, "\x00\\") || strings.HasSuffix(reference, "/") {
		return nil, false
	}
	parts := strings.Split(reference, "/")
	// A final dot denotes a directory, not a filename.
	if parts[len(parts)-1] == "." {
		return nil, false
	}
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "..":
			return nil, false
		case "", ".":
			continue
		default:
			normalized = append(normalized, part)
		}
	}
	return normalized, len(normalized) > 0
}

func pathMatchCandidateValid(candidate string) bool {
	if strings.ContainsAny(candidate, "\x00\\") {
		return false
	}
	for _, component := range strings.Split(candidate, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
