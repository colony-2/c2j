package artifacts

import (
	"fmt"
	"strings"

	"github.com/colony-2/swf-go/pkg/swf"
)

type RefKind string

const (
	RefKindStored   RefKind = "stored"
	RefKindExternal RefKind = "external"
)

type Ref struct {
	Kind     RefKind      `json:"kind"`
	Name     string       `json:"name,omitempty"`
	Stored   *StoredRef   `json:"stored,omitempty"`
	External *ExternalRef `json:"external,omitempty"`
}

type StoredRef struct {
	Key swf.ArtifactKey `json:"key"`
}

type ExternalRef struct {
	URL    string `json:"url"`
	Expand bool   `json:"expand,omitempty"`
}

func NewStoredRef(key swf.ArtifactKey) Ref {
	return Ref{
		Kind:   RefKindStored,
		Name:   strings.TrimSpace(key.Name),
		Stored: &StoredRef{Key: key},
	}
}

func NewExternalRef(name string, url string, expand bool) Ref {
	return Ref{
		Kind: RefKindExternal,
		Name: strings.TrimSpace(name),
		External: &ExternalRef{
			URL:    strings.TrimSpace(url),
			Expand: expand,
		},
	}
}

func RefFromArtifact(artifact swf.Artifact) (Ref, error) {
	if artifact == nil {
		return Ref{}, fmt.Errorf("artifact is nil")
	}
	key, err := artifact.ArtifactKey()
	if err != nil {
		return Ref{}, err
	}
	ref := NewStoredRef(key)
	if ref.Name == "" {
		ref.Name = strings.TrimSpace(artifact.Name())
	}
	return ref, nil
}

func (r Ref) Validate() error {
	switch r.Kind {
	case RefKindStored:
		if r.Stored == nil {
			return fmt.Errorf("stored ref missing payload")
		}
		if r.External != nil {
			return fmt.Errorf("stored ref cannot also have external payload")
		}
		if err := r.Stored.Key.Validate(); err != nil {
			return err
		}
		if r.NameValue() == "" {
			return fmt.Errorf("stored ref name cannot be empty")
		}
		return nil
	case RefKindExternal:
		if r.External == nil {
			return fmt.Errorf("external ref missing payload")
		}
		if r.Stored != nil {
			return fmt.Errorf("external ref cannot also have stored payload")
		}
		if r.NameValue() == "" {
			return fmt.Errorf("external ref name cannot be empty")
		}
		if strings.TrimSpace(r.External.URL) == "" {
			return fmt.Errorf("external ref url cannot be empty")
		}
		return nil
	default:
		return fmt.Errorf("invalid artifact ref kind %q", r.Kind)
	}
}

func (r Ref) NameValue() string {
	if name := strings.TrimSpace(r.Name); name != "" {
		return name
	}
	if r.Stored != nil {
		return strings.TrimSpace(r.Stored.Key.Name)
	}
	return ""
}

func (r Ref) StoredKey() (swf.ArtifactKey, bool) {
	if r.Kind != RefKindStored || r.Stored == nil {
		return swf.ArtifactKey{}, false
	}
	return r.Stored.Key, true
}

func (r Ref) SizeBytes() int64 {
	if key, ok := r.StoredKey(); ok {
		return key.SizeBytes
	}
	return -1
}

func (r Ref) Identity() string {
	if key, ok := r.StoredKey(); ok {
		return fmt.Sprintf("stored:%s:%d:%s", key.JobId, key.TaskOrdinal, key.Name)
	}
	if r.External != nil {
		return fmt.Sprintf("external:%s:%t:%s", r.External.URL, r.External.Expand, r.NameValue())
	}
	return fmt.Sprintf("invalid:%s:%s", r.Kind, r.NameValue())
}

func (r Ref) IsZero() bool {
	return r.Kind == "" && r.Name == "" && r.Stored == nil && r.External == nil
}
