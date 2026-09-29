// Package objects provides immutable, typed checkpoints backed by JobDB artifacts.
package objects

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

const ArtifactPrefix = "__c2j_objects__/"
const Marker = "$c2j_object"
const Version = "v1"

// Ref identifies an exact checkpoint. Metadata and files live in its artifact,
// never in the recipe value. The type includes its contract version, e.g.
// c2ops.codex.session/v1, independently of this reference format's version.
type Ref struct {
	Format   string            `json:"$c2j_object"`
	Type     string            `json:"type"`
	TenantID string            `json:"tenant_id"`
	Artifact jobdb.ArtifactKey `json:"artifact"`
	SHA256   string            `json:"sha256"`
}

func IsInternalArtifact(name string) bool { return strings.HasPrefix(name, ArtifactPrefix) }

func (r Ref) Validate() error {
	if r.Format != Version {
		return fmt.Errorf("unsupported object reference format %q", r.Format)
	}
	if err := validateType(r.Type); err != nil {
		return err
	}
	if r.Artifact.TaskOrdinal <= 0 || r.Artifact.SizeBytes < 0 {
		return fmt.Errorf("object checkpoint requires a task outcome and known archive size")
	}
	if r.TenantID == "" {
		return fmt.Errorf("object tenant is required")
	}
	if err := r.Artifact.Validate(); err != nil {
		return fmt.Errorf("object artifact: %w", err)
	}
	digest, err := hex.DecodeString(r.SHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(r.SHA256) != r.SHA256 {
		return fmt.Errorf("invalid object digest")
	}
	if r.Artifact.Name != ArtifactPrefix+r.SHA256+".tar" {
		return fmt.Errorf("object artifact name does not match digest")
	}
	return nil
}

var typePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/v[1-9][0-9]*$`)

func validateType(name string) error {
	if !typePattern.MatchString(name) {
		return fmt.Errorf("object type must include a version, e.g. c2ops.codex.session/v1")
	}
	return nil
}

// Parse recognizes both native references and their JSON representation.
func Parse(value any) (Ref, bool, error) {
	var r Ref
	switch v := value.(type) {
	case Ref:
		r = v
	case *Ref:
		if v == nil {
			return r, false, nil
		}
		r = *v
	case map[string]any:
		if _, ok := v[Marker]; !ok {
			return r, false, nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return r, true, err
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return r, true, err
		}
	default:
		return r, false, nil
	}
	return r, true, r.Validate()
}

// Transform walks nested inputs/outputs, retaining native scalar values.
// References are leaves: their storage descriptors are not walked as inputs.
func Transform(value any, fn func(Ref) (any, error)) (any, error) {
	if r, ok, err := Parse(value); ok {
		if err != nil {
			return nil, err
		}
		return fn(r)
	}
	if value == nil {
		return nil, nil
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String || v.IsNil() {
			return value, nil
		}
		out := make(map[string]any, v.Len())
		for _, key := range v.MapKeys() {
			item, err := Transform(v.MapIndex(key).Interface(), fn)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key.String(), err)
			}
			out[key.String()] = item
		}
		return out, nil
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 || (v.Kind() == reflect.Slice && v.IsNil()) {
			return value, nil
		}
		out := make([]any, v.Len())
		for i := range out {
			item, err := Transform(v.Index(i).Interface(), fn)
			if err != nil {
				return nil, err
			}
			out[i] = item
		}
		return out, nil
	case reflect.Struct, reflect.Pointer:
		// Native op structs may themselves contain object references.
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var decoded any
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&decoded); err != nil {
			return nil, err
		}
		return Transform(decoded, fn)
	default:
		return value, nil
	}
}

func Collect(value any) ([]Ref, error) {
	seen := map[string]Ref{}
	_, err := Transform(value, func(r Ref) (any, error) {
		key := fmt.Sprintf("%s/%s/%d/%s", r.TenantID, r.Artifact.JobId, r.Artifact.TaskOrdinal, r.SHA256)
		if old, ok := seen[key]; ok && old != r {
			return nil, fmt.Errorf("conflicting object references")
		}
		seen[key] = r
		return r, nil
	})
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Ref, 0, len(keys))
	for _, key := range keys {
		out = append(out, seen[key])
	}
	return out, nil
}
