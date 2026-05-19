package compiler

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/swf-go/pkg/swf"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

func resolveArtifactBindings(resCtx *template.ResolutionContext, bindings map[string]interface{}) (map[string]recipeartifacts.Ref, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	resolved, err := resCtx.ResolveMap(bindings)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve templates op artifacts: %w", err)
	}
	acc := newArtifactBindingAccumulator(len(resolved))
	names := sortedMapKeys(resolved)
	for _, name := range names {
		value := resolved[name]
		if name == "" {
			return nil, fmt.Errorf("artifact binding name cannot be empty")
		}
		artifactRef, err := coerceArtifactRef(value)
		if err == nil {
			materializedName, err := materializedExplicitBindingName(name, artifactRef)
			if err != nil {
				return nil, fmt.Errorf("artifact binding %q is invalid: %w", name, err)
			}
			if err := acc.add(name, materializedName, artifactRef, fmt.Sprintf("explicit binding %q", name)); err != nil {
				return nil, err
			}
			continue
		}

		artifactRefs, setErr := coerceArtifactSet(value)
		if setErr != nil {
			return nil, fmt.Errorf("artifact binding %q is invalid: expected artifact ref or artifact set (artifact ref error: %v; artifact set error: %v)", name, err, setErr)
		}
		prefix, err := artifactBindingPrefix(name)
		if err != nil {
			return nil, fmt.Errorf("artifact binding %q is invalid: %w", name, err)
		}
		for _, artifactRef := range artifactRefs {
			materializedName, err := joinArtifactBindingPath(prefix, artifactRef.NameValue())
			if err != nil {
				return nil, fmt.Errorf("artifact binding %q expansion for artifact %q is invalid: %w", name, artifactRef.NameValue(), err)
			}
			if err := acc.add(materializedName, materializedName, artifactRef, fmt.Sprintf("%q expansion", name)); err != nil {
				return nil, err
			}
		}
	}
	return acc.bindings(), nil
}

func coerceArtifactRef(value interface{}) (recipeartifacts.Ref, error) {
	switch v := value.(type) {
	case recipeartifacts.Ref:
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return v, nil
	case *recipeartifacts.Ref:
		if v == nil {
			return recipeartifacts.Ref{}, fmt.Errorf("artifact ref is nil")
		}
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return *v, nil
	case swf.ArtifactKey:
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return recipeartifacts.NewStoredRef(v), nil
	case *swf.ArtifactKey:
		if v == nil {
			return recipeartifacts.Ref{}, fmt.Errorf("artifact key is nil")
		}
		if err := v.Validate(); err != nil {
			return recipeartifacts.Ref{}, err
		}
		return recipeartifacts.NewStoredRef(*v), nil
	case ref.Val:
		if v == nil || v == types.NullValue {
			return recipeartifacts.Ref{}, fmt.Errorf("artifact ref is nil")
		}
		if _, ok := v.(traits.Lister); ok {
			return recipeartifacts.Ref{}, fmt.Errorf("expected artifact ref, got artifact set")
		}
		if _, ok := v.(traits.Mapper); ok {
			return recipeartifacts.Ref{}, fmt.Errorf("expected artifact ref, got artifact set")
		}
		return coerceArtifactRef(v.Value())
	case map[string]interface{}:
		return coerceArtifactRefFromMap(v)
	default:
		return recipeartifacts.Ref{}, fmt.Errorf("expected artifact ref, got %T", value)
	}
}

func coerceArtifactRefFromMap(value map[string]interface{}) (recipeartifacts.Ref, error) {
	var artifactRef recipeartifacts.Ref
	if err := decodeJSONTaggedMap(value, &artifactRef); err == nil {
		if artifactRef.Kind != "" || artifactRef.Name != "" || artifactRef.Stored != nil || artifactRef.External != nil {
			if err := artifactRef.Validate(); err != nil {
				return recipeartifacts.Ref{}, err
			}
			return artifactRef, nil
		}
	}

	var key swf.ArtifactKey
	if err := decodeJSONTaggedMap(value, &key); err == nil {
		if key.JobId != "" || key.TaskOrdinal != 0 || key.Name != "" || key.SizeBytes != 0 {
			if err := key.Validate(); err != nil {
				return recipeartifacts.Ref{}, err
			}
			return recipeartifacts.NewStoredRef(key), nil
		}
	}

	return recipeartifacts.Ref{}, fmt.Errorf("expected artifact ref, got map[string]interface {}")
}

func coerceArtifactSet(value interface{}) ([]recipeartifacts.Ref, error) {
	if value == nil {
		return nil, nil
	}

	switch v := value.(type) {
	case []recipeartifacts.Ref:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, item := range v {
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case []*recipeartifacts.Ref:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, item := range v {
			if item == nil {
				continue
			}
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, *item)
		}
		return out, nil
	case []swf.ArtifactKey:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, item := range v {
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, recipeartifacts.NewStoredRef(item))
		}
		return out, nil
	case []*swf.ArtifactKey:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, item := range v {
			if item == nil {
				continue
			}
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, recipeartifacts.NewStoredRef(*item))
		}
		return out, nil
	case []interface{}:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, item := range v {
			artifactRef, err := coerceArtifactRef(item)
			if err != nil {
				return nil, err
			}
			out = append(out, artifactRef)
		}
		return out, nil
	case map[string]recipeartifacts.Ref:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, key := range sortedRefMapKeys(v) {
			item := v[key]
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		return out, nil
	case map[string]*recipeartifacts.Ref:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, key := range sortedRefPtrMapKeys(v) {
			item := v[key]
			if item == nil {
				continue
			}
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, *item)
		}
		return out, nil
	case map[string]swf.ArtifactKey:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, key := range sortedArtifactKeyMapKeys(v) {
			item := v[key]
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, recipeartifacts.NewStoredRef(item))
		}
		return out, nil
	case map[string]*swf.ArtifactKey:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, key := range sortedArtifactKeyPtrMapKeys(v) {
			item := v[key]
			if item == nil {
				continue
			}
			if err := item.Validate(); err != nil {
				return nil, err
			}
			out = append(out, recipeartifacts.NewStoredRef(*item))
		}
		return out, nil
	case map[string]interface{}:
		out := make([]recipeartifacts.Ref, 0, len(v))
		for _, key := range sortedMapKeys(v) {
			artifactRef, err := coerceArtifactRef(v[key])
			if err != nil {
				return nil, err
			}
			out = append(out, artifactRef)
		}
		return out, nil
	case ref.Val:
		return coerceArtifactSetRefVal(v)
	default:
		return nil, fmt.Errorf("expected artifact set, got %T", value)
	}
}

func coerceArtifactSetRefVal(value ref.Val) ([]recipeartifacts.Ref, error) {
	if value == nil || value == types.NullValue {
		return nil, nil
	}

	if lister, ok := value.(traits.Lister); ok {
		sizeVal := lister.Size()
		size, ok := sizeVal.(types.Int)
		if !ok {
			return nil, fmt.Errorf("unsupported artifact set list size type %T", sizeVal)
		}
		out := make([]recipeartifacts.Ref, 0, int(size))
		for i := int64(0); i < int64(size); i++ {
			artifactRef, err := coerceArtifactRefFromRefVal(lister.Get(types.Int(i)))
			if err != nil {
				return nil, err
			}
			out = append(out, artifactRef)
		}
		return out, nil
	}

	if mapper, ok := value.(traits.Mapper); ok {
		keys := make([]string, 0)
		it := mapper.Iterator()
		for it.HasNext() == types.True {
			key := it.Next()
			keyString, ok := refValString(key)
			if !ok {
				return nil, fmt.Errorf("artifact set map key is not a string")
			}
			keys = append(keys, keyString)
		}
		sort.Strings(keys)
		out := make([]recipeartifacts.Ref, 0, len(keys))
		for _, key := range keys {
			artifactRef, err := coerceArtifactRefFromRefVal(mapper.Get(types.String(key)))
			if err != nil {
				return nil, err
			}
			out = append(out, artifactRef)
		}
		return out, nil
	}

	artifactRef, err := coerceArtifactRefFromRefVal(value)
	if err != nil {
		return nil, fmt.Errorf("expected artifact set, got %T", value.Value())
	}
	return []recipeartifacts.Ref{artifactRef}, nil
}

func coerceArtifactRefFromRefVal(value ref.Val) (recipeartifacts.Ref, error) {
	if value == nil || value == types.NullValue {
		return recipeartifacts.Ref{}, fmt.Errorf("artifact ref is nil")
	}
	return coerceArtifactRef(value.Value())
}

func refValString(value ref.Val) (string, bool) {
	if value == nil || value == types.NullValue {
		return "", false
	}
	if s, ok := value.(types.String); ok {
		return string(s), true
	}
	s, ok := value.Value().(string)
	return s, ok
}

func materializedExplicitBindingName(name string, artifactRef recipeartifacts.Ref) (string, error) {
	if hasTrailingSlash(name) {
		prefix, err := artifactBindingPrefix(name)
		if err != nil {
			return "", err
		}
		return joinArtifactBindingPath(prefix, artifactRef.NameValue())
	}
	if err := validateArtifactBindingName(name); err != nil {
		return "", err
	}
	return normalizeArtifactBindingName(name), nil
}

func artifactBindingPrefix(name string) (string, error) {
	if err := validateArtifactBindingName(name); err != nil {
		return "", err
	}
	cleaned := normalizeArtifactBindingName(name)
	if cleaned == "." {
		return "", nil
	}
	return strings.TrimRight(cleaned, "/\\"), nil
}

func joinArtifactBindingPath(prefix string, artifactName string) (string, error) {
	if strings.TrimSpace(artifactName) == "" {
		return "", fmt.Errorf("artifact name cannot be empty")
	}
	if err := validateArtifactBindingName(artifactName); err != nil {
		return "", fmt.Errorf("invalid artifact name: %w", err)
	}
	var name string
	if prefix == "" {
		name = artifactName
	} else {
		name = filepath.Join(prefix, artifactName)
	}
	if err := validateArtifactBindingName(name); err != nil {
		return "", err
	}
	return normalizeArtifactBindingName(name), nil
}

func validateArtifactBindingName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("name must be relative")
	}
	for _, segment := range splitArtifactBindingPathSegments(name) {
		if segment == ".." {
			return fmt.Errorf("name must not contain '..' segments")
		}
	}
	return nil
}

func normalizeArtifactBindingName(name string) string {
	return filepath.ToSlash(filepath.Clean(name))
}

func splitArtifactBindingPathSegments(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

func hasTrailingSlash(name string) bool {
	return strings.HasSuffix(name, "/") || strings.HasSuffix(name, "\\")
}

type artifactBindingAccumulator struct {
	out    map[string]recipeartifacts.Ref
	claims map[string]string
}

func newArtifactBindingAccumulator(size int) *artifactBindingAccumulator {
	return &artifactBindingAccumulator{
		out:    make(map[string]recipeartifacts.Ref, size),
		claims: make(map[string]string, size),
	}
}

func (a *artifactBindingAccumulator) add(bindingName string, materializedName string, artifactRef recipeartifacts.Ref, source string) error {
	if err := validateArtifactBindingName(bindingName); err != nil {
		return fmt.Errorf("artifact binding %q is invalid: %w", bindingName, err)
	}
	if err := validateArtifactBindingName(materializedName); err != nil {
		return fmt.Errorf("artifact binding %q is invalid: %w", materializedName, err)
	}
	normalizedMaterialized := normalizeArtifactBindingName(materializedName)
	if existingSource, exists := a.claims[normalizedMaterialized]; exists {
		return fmt.Errorf("artifact binding destination %q is defined by both %s and %s", normalizedMaterialized, existingSource, source)
	}
	for existingName, existingSource := range a.claims {
		switch {
		case artifactBindingPathContains(existingName, normalizedMaterialized):
			return fmt.Errorf("artifact binding destination %q conflicts with %s at %q", normalizedMaterialized, existingSource, existingName)
		case artifactBindingPathContains(normalizedMaterialized, existingName):
			return fmt.Errorf("artifact binding destination %q conflicts with %s at %q", normalizedMaterialized, existingSource, existingName)
		}
	}
	a.claims[normalizedMaterialized] = source
	a.out[bindingName] = artifactRef
	return nil
}

func (a *artifactBindingAccumulator) bindings() map[string]recipeartifacts.Ref {
	if len(a.out) == 0 {
		return nil
	}
	return a.out
}

func artifactBindingPathContains(parent string, child string) bool {
	if parent == child {
		return false
	}
	parent = normalizeArtifactBindingName(parent)
	child = normalizeArtifactBindingName(child)
	return strings.HasPrefix(child, parent+"/")
}

func sortedMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedRefMapKeys(m map[string]recipeartifacts.Ref) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedRefPtrMapKeys(m map[string]*recipeartifacts.Ref) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedArtifactKeyMapKeys(m map[string]swf.ArtifactKey) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedArtifactKeyPtrMapKeys(m map[string]*swf.ArtifactKey) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
