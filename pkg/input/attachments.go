package input

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/c2j/pkg/ops"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// artifactBinder snapshots new attachments and verifies stored references using
// the current tenant. It never reads a caller-supplied filesystem path from JSON.
type artifactBinder struct {
	ctx       context.Context
	job       jobdb.JobKey
	ordinal   int64
	resolve   func(jobdb.ArtifactKey) jobdb.Artifact
	refs      map[string]recipeartifacts.Ref
	reserved  map[string]bool
	artifacts []jobdb.Artifact
	checked   map[string]bool
}

func newArtifactBinder(ctx context.Context, deps ops.OpDependencies, reserved []jobdb.Artifact) *artifactBinder {
	b := &artifactBinder{ctx: ctx, refs: map[string]recipeartifacts.Ref{}, reserved: map[string]bool{}}
	if tool := deps.JobTool(); tool != nil {
		b.job = tool.GetJobKey()
		if task, ok := tool.(interface{ TaskOrdinal() int64 }); ok {
			b.ordinal = task.TaskOrdinal()
		}
	}
	b.resolve = func(key jobdb.ArtifactKey) jobdb.Artifact {
		if deps.WorkflowControl() == nil {
			return nil
		}
		return deps.WorkflowControl().GetArtifactLazy(ctx, b.job.TenantId, key)
	}
	for _, a := range reserved {
		b.reserved[a.Name()] = true
	}
	return b
}

func (b *artifactBinder) cleanup() {
	for _, a := range b.artifacts {
		_ = a.Cleanup()
	}
}

func (b *artifactBinder) addRef(name string, ref recipeartifacts.Ref) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("attachment name is required")
	}
	if old, ok := b.refs[name]; ok && old.Identity() != ref.Identity() {
		return fmt.Errorf("attachment name %q refers to different artifacts; use distinct binding names", name)
	}
	b.refs[name] = ref
	return nil
}

func (b *artifactBinder) checkRef(ref recipeartifacts.Ref) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	key, ok := ref.StoredKey()
	if !ok {
		return fmt.Errorf("structured input requires stored artifacts; snapshot external resources first")
	}
	if b.checked == nil {
		b.checked = map[string]bool{}
	}
	if b.checked[ref.Identity()] {
		return nil
	}
	if key.JobId == b.job.JobId && key.TaskOrdinal == b.ordinal {
		return fmt.Errorf("attachment %q has not been bound to this outcome", key.Name)
	}
	a := b.resolve(key)
	if a == nil {
		return fmt.Errorf("attachment %q is unavailable", key.Name)
	}
	r, err := a.Open()
	if err != nil {
		return fmt.Errorf("attachment %q: %w", key.Name, err)
	}
	defer r.Close()
	if _, err := io.Copy(io.Discard, r); err != nil {
		return fmt.Errorf("attachment %q: %w", key.Name, err)
	}
	b.checked[ref.Identity()] = true
	return nil
}

func (b *artifactBinder) bind(value any) (any, error) {
	if err := b.ctx.Err(); err != nil {
		return nil, err
	}
	if a, ok := value.(jobdb.Artifact); ok {
		if reflect.ValueOf(a).Kind() == reflect.Ptr && reflect.ValueOf(a).IsNil() {
			return nil, fmt.Errorf("nil attachment")
		}
		if key, err := a.ArtifactKey(); err == nil {
			return b.bind(recipeartifacts.NewStoredRef(key))
		}
		if b.job.TenantId == "" || b.job.JobId == "" || b.ordinal <= 0 {
			return nil, fmt.Errorf("new attachment requires a durable task identity")
		}
		if b.reserved[a.Name()] {
			return nil, fmt.Errorf("duplicate attachment name %q", a.Name())
		}
		f, err := os.CreateTemp("", "c2j-input-attachment-*")
		if err != nil {
			return nil, err
		}
		name := f.Name()
		if err := a.WriteTo(b.ctx, f); err != nil {
			_ = f.Close()
			_ = os.Remove(name)
			return nil, err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(name)
			return nil, err
		}
		frozen, err := jobdb.NewArtifactFromFile(a.Name(), name)
		if err != nil {
			_ = os.Remove(name)
			return nil, err
		}
		b.artifacts = append(b.artifacts, frozen)
		key := jobdb.ArtifactKey{JobId: b.job.JobId, TaskOrdinal: b.ordinal, Name: a.Name(), SizeBytes: frozen.Size()}
		ref := recipeartifacts.NewStoredRef(key)
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		if err := b.addRef(a.Name(), ref); err != nil {
			return nil, err
		}
		b.reserved[a.Name()] = true
		if b.checked == nil {
			b.checked = map[string]bool{}
		}
		b.checked[ref.Identity()] = true
		return jsonValue(ref)
	}
	switch v := value.(type) {
	case map[string]any:
		if v["kind"] == string(recipeartifacts.RefKindStored) || v["kind"] == string(recipeartifacts.RefKindExternal) {
			raw, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			var ref recipeartifacts.Ref
			if err := json.Unmarshal(raw, &ref); err != nil {
				return nil, err
			}
			if err := b.checkRef(ref); err != nil {
				return nil, err
			}
			if err := b.addRef(ref.NameValue(), ref); err != nil {
				return nil, err
			}
			return jsonValue(ref)
		}
		out := make(map[string]any, len(v))
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			resolved, err := b.bind(v[key])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			out[key] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			resolved, err := b.bind(item)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	case nil, string, bool, float64:
		return v, nil
	default:
		v, err := jsonValue(value)
		if err != nil {
			return nil, err
		}
		return b.bind(v)
	}
}
