// Package grouping defines the versioned recipe contribution envelope.
package grouping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/colony-2/c2j/internal/jsonexact"
	"github.com/colony-2/c2j/pkg/artifacts"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

// Spec keeps omission distinct from explicit null until resolved validation.
type Spec struct {
	Present bool `json:"-"`
	Value   any  `json:"-"`
}

func (s Spec) IsZero() bool                 { return !s.Present }
func (s Spec) MarshalJSON() ([]byte, error) { return json.Marshal(s.Value) }
func (s *Spec) UnmarshalJSON(raw []byte) error {
	s.Present = true
	if !utf8.Valid(raw) {
		return fmt.Errorf("grouping must be valid UTF-8")
	}
	return jsonexact.Unmarshal(raw, &s.Value)
}
func (s Spec) MarshalYAML() (any, error) { return s.Value, nil }
func (s *Spec) UnmarshalYAML(n *yaml.Node) error {
	s.Present = true
	return n.Decode(&s.Value)
}

type Contribution struct {
	Content   map[string]any  `json:"content,omitempty"`
	Artifacts []artifacts.Ref `json:"artifacts"`
}

// MarshalJSON preserves an explicitly empty content object.
func (c Contribution) MarshalJSON() ([]byte, error) {
	m := map[string]any{"artifacts": c.Artifacts}
	if c.Content != nil {
		m["content"] = c.Content
	}
	return json.Marshal(m)
}

// Context snapshots use ordinary json.Unmarshal. Retain the same exact native
// numbers as the initial grouped input decoder when restoring those snapshots.
func (c *Contribution) UnmarshalJSON(raw []byte) error {
	type wire Contribution
	var decoded wire
	if err := jsonexact.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	if decoded.Content != nil {
		decoded.Content = jsonexact.Native(decoded.Content).(map[string]any)
	}
	*c = Contribution(decoded)
	return nil
}

type Context struct {
	Appends []Contribution `json:"appends"`
}
type Prepared struct {
	Key          string
	MaxItems     int
	Contribution Contribution
	Native       []jobdb.ArtifactKey
}

func (s Spec) Prepare() (Prepared, error) {
	var p Prepared
	m, ok := s.Value.(map[string]any)
	if !ok || m == nil {
		return p, fmt.Errorf("grouping must be an object")
	}
	for key := range m {
		switch key {
		case "key", "content", "artifacts", "max_items":
		default:
			return p, fmt.Errorf("grouping.%s is unknown", key)
		}
	}
	p.Key, ok = m["key"].(string)
	if !ok || strings.TrimSpace(p.Key) == "" || !utf8.ValidString(p.Key) || strings.ContainsRune(p.Key, 0) || len(p.Key) > 1024 {
		return p, fmt.Errorf("grouping.key must be a nonblank UTF-8 string of 1–1024 bytes without NUL")
	}
	p.MaxItems = jobdb.DefaultMaxAppendInputs
	if value, exists := m["max_items"]; exists {
		raw, err := json.Marshal(value)
		var n int
		if bytes.Equal(raw, []byte("null")) || err != nil || json.Unmarshal(raw, &n) != nil || n < 0 || n > jobdb.MaxAppendInputs {
			return p, fmt.Errorf("grouping.max_items must be an integer from 0 through 1000")
		}
		if n != 0 {
			p.MaxItems = n
		}
	}
	c, native, err := DecodeContribution(m, true)
	p.Contribution, p.Native = c, native
	return p, err
}

// DecodeContribution validates both authored requests and persisted envelopes.
func DecodeContribution(m map[string]any, specification bool) (Contribution, []jobdb.ArtifactKey, error) {
	c := Contribution{Artifacts: []artifacts.Ref{}}
	if !specification {
		if _, exists := m["artifacts"]; !exists {
			return c, nil, fmt.Errorf("grouping append requires artifacts array")
		}
		for key := range m {
			if key != "content" && key != "artifacts" {
				return c, nil, fmt.Errorf("grouping append.%s is unknown", key)
			}
		}
	}
	if value, exists := m["content"]; exists {
		var ok bool
		c.Content, ok = value.(map[string]any)
		if !ok || c.Content == nil {
			return c, nil, fmt.Errorf("grouping.content must be a non-null object")
		}
	}
	if value, exists := m["artifacts"]; exists {
		// Accept typed slices from Go callers as well as resolved recipe lists.
		raw, err := json.Marshal(value)
		if err != nil || len(raw) == 0 || raw[0] != '[' {
			return c, nil, fmt.Errorf("grouping.artifacts must be an array")
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c.Artifacts); err != nil {
			return c, nil, fmt.Errorf("grouping.artifacts: %w", err)
		}
	}
	if c.Content == nil && len(c.Artifacts) == 0 {
		return c, nil, fmt.Errorf("grouping requires content or at least one artifact")
	}
	names := map[string]bool{}
	stored := map[string]jobdb.ArtifactKey{}
	native := []jobdb.ArtifactKey{}
	for i, ref := range c.Artifacts {
		invalid := func(err error) (Contribution, []jobdb.ArtifactKey, error) {
			return c, nil, fmt.Errorf("grouping.artifacts[%d]: %w", i, err)
		}
		if ref.Kind == artifacts.RefKindAttached {
			return invalid(fmt.Errorf("attached references are unresolved; provide a concrete stored, external or value reference"))
		}
		if err := ref.Validate(); err != nil {
			return invalid(err)
		}
		if err := cleanName(ref.NameValue()); err != nil {
			return invalid(err)
		}
		if ref.Name != "" {
			if err := cleanName(ref.Name); err != nil {
				return invalid(err)
			}
		}
		if names[ref.NameValue()] {
			return invalid(fmt.Errorf("duplicate effective name %q", ref.NameValue()))
		}
		names[ref.NameValue()] = true
		if key, ok := ref.StoredKey(); ok {
			if err := cleanName(key.Name); err != nil {
				return invalid(err)
			}
			if prior, exists := stored[key.Name]; exists {
				if prior != key {
					return invalid(fmt.Errorf("distinct stored keys share original name %q", key.Name))
				}
			} else {
				stored[key.Name] = key
				native = append(native, key)
			}
		}
	}
	return c, native, nil
}

func cleanName(name string) error {
	if name == "" || name == "." || name == ".." || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00") || !utf8.ValidString(name) {
		return fmt.Errorf("artifact name %q must be a clean relative path", name)
	}
	return nil
}
