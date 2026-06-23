package colonycel

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
)

func RegisterArtifactFunctions(b *funcregistry.Builder) {
	if b == nil {
		return
	}

	b.WithBuiltin("artifact_set", func(adapter types.Adapter, _ funcregistry.ContextProvider) cel.EnvOption {
		return artifactSetLikeEnvOption(adapter, "artifact_set")
	})
	b.WithBuiltin("artifact_concat", func(adapter types.Adapter, _ funcregistry.ContextProvider) cel.EnvOption {
		return artifactSetLikeEnvOption(adapter, "artifact_concat")
	})
	b.WithBuiltin("artifact_filter", func(adapter types.Adapter, _ funcregistry.ContextProvider) cel.EnvOption {
		return cel.Function(
			"artifact_filter",
			cel.Overload(
				"artifact_filter_dyn_dyn",
				[]*cel.Type{cel.DynType, cel.DynType},
				cel.ListType(cel.DynType),
				cel.BinaryBinding(func(arts ref.Val, opts ref.Val) ref.Val {
					artifactRefs, err := normalizeArtifactRefs("artifact_filter", arts)
					if err != nil {
						return types.NewErr("%v", err)
					}

					f, err := parseArtifactFilterOpts(opts)
					if err != nil {
						return types.NewErr("%v", err)
					}
					filtered := make([]recipeartifacts.Ref, 0, len(artifactRefs))
					for _, artifactRef := range artifactRefs {
						if f.match(artifactRef) {
							filtered = append(filtered, artifactRef)
						}
					}
					return adapter.NativeToValue(filtered)
				}),
			),
		)
	})
	b.WithBuiltin("artifact_names", func(adapter types.Adapter, _ funcregistry.ContextProvider) cel.EnvOption {
		return cel.Function(
			"artifact_names",
			cel.Overload(
				"artifact_names_dyn",
				[]*cel.Type{cel.DynType},
				cel.ListType(cel.StringType),
				cel.UnaryBinding(func(arts ref.Val) ref.Val {
					artifactRefs, err := normalizeArtifactRefs("artifact_names", arts)
					if err != nil {
						return types.NewErr("%v", err)
					}
					out := make([]string, 0, len(artifactRefs))
					for _, artifactRef := range artifactRefs {
						out = append(out, artifactRef.NameValue())
					}
					return adapter.NativeToValue(out)
				}),
			),
		)
	})
	b.WithBuiltin("artifact_unique", func(adapter types.Adapter, _ funcregistry.ContextProvider) cel.EnvOption {
		return cel.Function(
			"artifact_unique",
			cel.Overload(
				"artifact_unique_dyn",
				[]*cel.Type{cel.DynType},
				cel.ListType(cel.DynType),
				cel.UnaryBinding(func(arts ref.Val) ref.Val {
					artifactRefs, err := normalizeArtifactRefs("artifact_unique", arts)
					if err != nil {
						return types.NewErr("%v", err)
					}
					return adapter.NativeToValue(dedupeArtifactRefs(artifactRefs, "name"))
				}),
			),
			cel.Overload(
				"artifact_unique_dyn_string",
				[]*cel.Type{cel.DynType, cel.StringType},
				cel.ListType(cel.DynType),
				cel.BinaryBinding(func(arts ref.Val, byVal ref.Val) ref.Val {
					artifactRefs, err := normalizeArtifactRefs("artifact_unique", arts)
					if err != nil {
						return types.NewErr("%v", err)
					}
					by, ok := toString(byVal)
					if !ok || strings.TrimSpace(by) == "" {
						by = "name"
					}
					out, err := dedupeArtifactRefsChecked(artifactRefs, by)
					if err != nil {
						return types.NewErr("artifact_unique: %v", err)
					}
					return adapter.NativeToValue(out)
				}),
			),
		)
	})
}

func artifactSetLikeEnvOption(adapter types.Adapter, fnName string) cel.EnvOption {
	return cel.Function(
		fnName,
		cel.Overload(
			fnName+"_dyn",
			[]*cel.Type{cel.DynType},
			cel.ListType(cel.DynType),
			cel.UnaryBinding(func(a ref.Val) ref.Val {
				artifactRefs, err := normalizeArtifactRefs(fnName, a)
				if err != nil {
					return types.NewErr("%v", err)
				}
				return adapter.NativeToValue(artifactRefs)
			}),
		),
		cel.Overload(
			fnName+"_dyn_dyn",
			[]*cel.Type{cel.DynType, cel.DynType},
			cel.ListType(cel.DynType),
			cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				var out []recipeartifacts.Ref
				if err := appendArtifactRefs(fnName, &out, a); err != nil {
					return types.NewErr("%v", err)
				}
				if err := appendArtifactRefs(fnName, &out, b); err != nil {
					return types.NewErr("%v", err)
				}
				return adapter.NativeToValue(out)
			}),
		),
		cel.Overload(
			fnName+"_dyn_dyn_dyn",
			[]*cel.Type{cel.DynType, cel.DynType, cel.DynType},
			cel.ListType(cel.DynType),
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				var out []recipeartifacts.Ref
				for _, a := range args {
					if err := appendArtifactRefs(fnName, &out, a); err != nil {
						return types.NewErr("%v", err)
					}
				}
				return adapter.NativeToValue(out)
			}),
		),
		cel.Overload(
			fnName+"_dyn_dyn_dyn_dyn",
			[]*cel.Type{cel.DynType, cel.DynType, cel.DynType, cel.DynType},
			cel.ListType(cel.DynType),
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				var out []recipeartifacts.Ref
				for _, a := range args {
					if err := appendArtifactRefs(fnName, &out, a); err != nil {
						return types.NewErr("%v", err)
					}
				}
				return adapter.NativeToValue(out)
			}),
		),
	)
}

func normalizeArtifactRefs(fnName string, v ref.Val) ([]recipeartifacts.Ref, error) {
	out := []recipeartifacts.Ref{}
	if err := appendArtifactRefs(fnName, &out, v); err != nil {
		return nil, err
	}
	return out, nil
}

func appendArtifactRefs(fnName string, out *[]recipeartifacts.Ref, v ref.Val) error {
	if v == nil || v == types.NullValue {
		return nil
	}

	if l, ok := v.(traits.Lister); ok {
		sizeVal := l.Size()
		size, ok := sizeVal.(types.Int)
		if !ok {
			return fmt.Errorf("%s: unsupported input type %T", fnName, v.Value())
		}
		for i := int64(0); i < int64(size); i++ {
			item := l.Get(types.Int(i))
			if err := appendArtifactRefs(fnName, out, item); err != nil {
				return err
			}
		}
		return nil
	}

	if m, ok := v.(traits.Mapper); ok {
		it := m.Iterator()
		keys := make([]string, 0)
		for it.HasNext() == types.True {
			k := it.Next()
			ks, ok := toString(k)
			if !ok {
				return fmt.Errorf("%s: unsupported input type %T", fnName, v.Value())
			}
			keys = append(keys, ks)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := m.Get(types.String(k))
			if err := appendArtifactRefs(fnName, out, val); err != nil {
				return err
			}
		}
		return nil
	}

	return appendArtifactRefsNative(fnName, out, v.Value())
}

func appendArtifactRefsNative(fnName string, out *[]recipeartifacts.Ref, native any) error {
	if native == nil {
		return nil
	}

	switch v := native.(type) {
	case recipeartifacts.Ref:
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%s: %w", fnName, err)
		}
		*out = append(*out, v)
		return nil
	case *recipeartifacts.Ref:
		if v == nil {
			return nil
		}
		return appendArtifactRefsNative(fnName, out, *v)
	}

	if keyer, ok := native.(interface {
		ArtifactKey() (jobdb.ArtifactKey, error)
	}); ok {
		key, err := keyer.ArtifactKey()
		if err != nil {
			return fmt.Errorf("%s: %w", fnName, err)
		}
		if err := key.Validate(); err != nil {
			return fmt.Errorf("%s: %w", fnName, err)
		}
		*out = append(*out, recipeartifacts.NewStoredRef(key))
		return nil
	}

	switch v := native.(type) {
	case jobdb.ArtifactKey:
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%s: %w", fnName, err)
		}
		*out = append(*out, recipeartifacts.NewStoredRef(v))
		return nil
	case *jobdb.ArtifactKey:
		if v == nil {
			return nil
		}
		return appendArtifactRefsNative(fnName, out, *v)
	case []recipeartifacts.Ref:
		for _, item := range v {
			if err := appendArtifactRefsNative(fnName, out, item); err != nil {
				return err
			}
		}
		return nil
	case []*recipeartifacts.Ref:
		for _, item := range v {
			if err := appendArtifactRefsNative(fnName, out, item); err != nil {
				return err
			}
		}
		return nil
	case []jobdb.ArtifactKey:
		for _, item := range v {
			if err := appendArtifactRefsNative(fnName, out, item); err != nil {
				return err
			}
		}
		return nil
	case []*jobdb.ArtifactKey:
		for _, item := range v {
			if err := appendArtifactRefsNative(fnName, out, item); err != nil {
				return err
			}
		}
		return nil
	case []interface{}:
		for _, item := range v {
			if err := appendArtifactRefsNative(fnName, out, item); err != nil {
				return err
			}
		}
		return nil
	case []ref.Val:
		for _, item := range v {
			if err := appendArtifactRefs(fnName, out, item); err != nil {
				return err
			}
		}
		return nil
	case map[string]recipeartifacts.Ref:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := appendArtifactRefsNative(fnName, out, v[k]); err != nil {
				return err
			}
		}
		return nil
	case map[string]*recipeartifacts.Ref:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := appendArtifactRefsNative(fnName, out, v[k]); err != nil {
				return err
			}
		}
		return nil
	case map[string]jobdb.ArtifactKey:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := appendArtifactRefsNative(fnName, out, v[k]); err != nil {
				return err
			}
		}
		return nil
	case map[string]*jobdb.ArtifactKey:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := appendArtifactRefsNative(fnName, out, v[k]); err != nil {
				return err
			}
		}
		return nil
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := appendArtifactRefsNative(fnName, out, v[k]); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%s: unsupported input type %T", fnName, native)
	}
}

type artifactFilter struct {
	namePrefix   string
	nameSuffix   string
	nameContains string
	nameRegex    *regexp.Regexp
	minSize      *int64
	maxSize      *int64
}

func parseArtifactFilterOpts(opts ref.Val) (artifactFilter, error) {
	if opts == nil || opts == types.NullValue {
		return artifactFilter{}, nil
	}
	m, ok := opts.(traits.Mapper)
	if !ok {
		return artifactFilter{}, fmt.Errorf("artifact_filter: expected opts to be a map")
	}
	f := artifactFilter{}

	if s, ok := mapGetString(m, "name_prefix"); ok {
		f.namePrefix = s
	}
	if s, ok := mapGetString(m, "name_suffix"); ok {
		f.nameSuffix = s
	}
	if s, ok := mapGetString(m, "name_contains"); ok {
		f.nameContains = s
	}
	if s, ok := mapGetString(m, "name_regex"); ok && strings.TrimSpace(s) != "" {
		re, err := regexp.Compile(s)
		if err != nil {
			return artifactFilter{}, fmt.Errorf("artifact_filter: invalid name_regex: %v", err)
		}
		f.nameRegex = re
	}

	if i, ok := mapGetInt64(m, "min_size"); ok {
		f.minSize = &i
	}
	if i, ok := mapGetInt64(m, "max_size"); ok {
		f.maxSize = &i
	}

	return f, nil
}

func (f artifactFilter) match(artifactRef recipeartifacts.Ref) bool {
	name := artifactRef.NameValue()
	if f.namePrefix != "" && !strings.HasPrefix(name, f.namePrefix) {
		return false
	}
	if f.nameSuffix != "" && !strings.HasSuffix(name, f.nameSuffix) {
		return false
	}
	if f.nameContains != "" && !strings.Contains(name, f.nameContains) {
		return false
	}
	if f.nameRegex != nil && !f.nameRegex.MatchString(name) {
		return false
	}

	size := artifactRef.SizeBytes()
	if f.minSize != nil {
		if size < 0 && *f.minSize > -1 {
			return false
		}
		if size >= 0 && size < *f.minSize {
			return false
		}
	}
	if f.maxSize != nil {
		if size >= 0 && size > *f.maxSize {
			return false
		}
		if size < 0 && *f.maxSize < 0 {
			return false
		}
	}

	return true
}

func dedupeArtifactRefs(refs []recipeartifacts.Ref, by string) []recipeartifacts.Ref {
	out, err := dedupeArtifactRefsChecked(refs, by)
	if err != nil {
		out, _ = dedupeArtifactRefsChecked(refs, "name")
	}
	return out
}

func dedupeArtifactRefsChecked(refs []recipeartifacts.Ref, by string) ([]recipeartifacts.Ref, error) {
	switch by {
	case "name", "":
		seen := map[string]struct{}{}
		out := make([]recipeartifacts.Ref, 0, len(refs))
		for _, artifactRef := range refs {
			name := artifactRef.NameValue()
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, artifactRef)
		}
		return out, nil
	case "key":
		seen := map[string]struct{}{}
		out := make([]recipeartifacts.Ref, 0, len(refs))
		for _, artifactRef := range refs {
			id := artifactRef.Identity()
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, artifactRef)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("invalid by: %s", by)
	}
}

func mapGetString(m traits.Mapper, key string) (string, bool) {
	v := m.Get(types.String(key))
	if v == nil || v == types.NullValue {
		return "", false
	}
	s, ok := toString(v)
	return s, ok
}

func mapGetInt64(m traits.Mapper, key string) (int64, bool) {
	v := m.Get(types.String(key))
	if v == nil || v == types.NullValue {
		return 0, false
	}
	i, ok := toInt64(v)
	return i, ok
}

func toString(v ref.Val) (string, bool) {
	if v == nil || v == types.NullValue {
		return "", false
	}
	switch s := v.(type) {
	case types.String:
		return string(s), true
	default:
		if raw, ok := v.Value().(string); ok {
			return raw, true
		}
		return "", false
	}
}

func toInt64(v ref.Val) (int64, bool) {
	if v == nil || v == types.NullValue {
		return 0, false
	}
	switch n := v.(type) {
	case types.Int:
		return int64(n), true
	case types.Uint:
		return int64(n), true
	default:
		switch raw := v.Value().(type) {
		case int:
			return int64(raw), true
		case int64:
			return raw, true
		case float64:
			return int64(raw), true
		default:
			return 0, false
		}
	}
}
