package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/colony-2/c2j/pkg/objects"
	"github.com/colony-2/c2j/pkg/ops"
)

const objectOutboxEnv = "C2J_OBJECT_OUTBOX"

type objectDraft struct {
	Type     string            `json:"type"`
	Metadata any               `json:"metadata"`
	Files    map[string]string `json:"files"`
}

func objectPaths(deps ops.OpDependencies) ops.OperationPaths {
	if p, ok := deps.(ops.OperationPathProvider); ok {
		return p.OperationPaths()
	}
	return ops.OperationPaths{}
}

func validateObjectPath(file, root string) (string, error) {
	if root == "" || !filepath.IsAbs(file) {
		return "", fmt.Errorf("object path must be absolute and within the invocation directory")
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("object path %q escapes staging directory", file)
	}
	return filepath.Join(root, rel), nil
}

func hydrateObjects(ctx context.Context, deps ops.OpDependencies, payload map[string]any) (map[string]any, error) {
	views := objectPaths(deps)
	result, err := objects.Transform(payload, func(ref objects.Ref) (any, error) {
		if deps.Objects() == nil {
			return nil, fmt.Errorf("object storage unavailable")
		}
		snapshot, err := deps.Objects().Open(ctx, ref, ref.Type)
		if err != nil {
			return nil, err
		}
		for name, file := range snapshot.Files {
			mapped, err := validateObjectPath(file, views.Workdir)
			if err != nil {
				return nil, err
			}
			snapshot.Files[name] = mapped
		}
		// The subprocess sees only named parts, not the internal staging directory.
		return snapshot, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func prepareObjectOutbox(deps ops.OpDependencies, env map[string]string) error {
	if deps.Objects() == nil {
		return nil
	}
	views := objectPaths(deps)
	if views.Workdir == "" {
		return fmt.Errorf("object storage requires invocation paths")
	}
	host := filepath.Join(views.Workdir, "objects-out")
	if err := os.MkdirAll(host, 0700); err != nil {
		return err
	}
	env[objectOutboxEnv] = filepath.Join(views.Workdir, "objects-out")
	return nil
}

// publishExtensionObjects consumes only the reserved objects envelope. Plain
// output remains backward compatible, including fields named "objects".
func publishExtensionObjects(ctx context.Context, deps ops.OpDependencies, stdout []byte, outputs map[string]any) (map[string]any, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return nil, err
	}
	if _, wrapped := envelope["output"]; !wrapped {
		return outputs, nil
	}
	raw, exists := envelope["objects"]
	drafts := map[string]objectDraft{}
	if exists {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		dec.UseNumber()
		if err := dec.Decode(&drafts); err != nil {
			return nil, err
		}
	}
	// Check every marker before publishing anything; reject unused drafts as likely
	// mistakes instead of silently storing unreachable checkpoints.
	used := map[string]bool{}
	_, err := replaceObjectMarkers(outputs, func(name string) (any, error) {
		if _, ok := drafts[name]; !ok {
			return nil, fmt.Errorf("object output refers to unknown draft %q", name)
		}
		used[name] = true
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if len(used) != len(drafts) {
		return nil, fmt.Errorf("unreferenced object draft")
	}
	names := make([]string, 0, len(drafts))
	for name := range drafts {
		names = append(names, name)
	}
	sort.Strings(names)
	views := objectPaths(deps)
	for _, name := range names {
		draft := drafts[name]
		for part, file := range draft.Files {
			host, err := validateObjectPath(file, filepath.Join(views.Workdir, "objects-out"))
			if err != nil {
				return nil, err
			}
			real, err := filepath.EvalSymlinks(host)
			if err != nil {
				return nil, err
			}
			staging := filepath.Join(views.Workdir, "objects-out")
			info, err := os.Lstat(staging)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				return nil, fmt.Errorf("object staging directory was replaced")
			}
			root, err := filepath.EvalSymlinks(staging)
			if err != nil {
				return nil, err
			}
			relative, err := filepath.Rel(staging, host)
			if err != nil {
				return nil, err
			}
			if _, err := validateObjectPath(real, root); err != nil {
				return nil, err
			}
			if filepath.Clean(real) != filepath.Join(root, relative) {
				return nil, fmt.Errorf("object part path contains a symlink")
			}
			draft.Files[part] = host
		}
		drafts[name] = draft
	}
	refs := map[string]objects.Ref{}
	for _, name := range names {
		if deps.Objects() == nil {
			return nil, fmt.Errorf("object storage unavailable")
		}
		draft := drafts[name]
		ref, err := deps.Objects().Publish(ctx, draft.Type, draft.Metadata, draft.Files)
		if err != nil {
			return nil, fmt.Errorf("publish object %q: %w", name, err)
		}
		refs[name] = ref
	}
	result, err := replaceObjectMarkers(outputs, func(name string) (any, error) { return objects.JSONValue(refs[name]) })
	if err != nil {
		return nil, err
	}
	out := result.(map[string]any)
	if _, err := objects.Collect(out); err != nil {
		return nil, err
	}
	return out, nil
}

func replaceObjectMarkers(value any, resolve func(string) (any, error)) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		if marker, ok := v["$object"]; ok {
			name, ok := marker.(string)
			if !ok || name == "" || len(v) != 1 {
				return nil, fmt.Errorf("object output marker must contain only a nonempty $object name")
			}
			return resolve(name)
		}
		out := map[string]any{}
		for k, item := range v {
			n, err := replaceObjectMarkers(item, resolve)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			n, err := replaceObjectMarkers(item, resolve)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	default:
		return value, nil
	}
}
