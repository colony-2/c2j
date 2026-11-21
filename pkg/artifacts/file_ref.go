package artifacts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// ParseFileRef accepts only the public, unexpanded file-reference contract.
// Check members before conversion: decoding straight into Ref would silently
// discard unknown members and infer missing integer fields from zero values.
func ParseFileRef(value any) (Ref, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return Ref{}, fmt.Errorf("malformed artifact file reference: %w", err)
	}
	var decoded any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&decoded); err != nil {
		return Ref{}, fmt.Errorf("malformed artifact file reference: %w", err)
	}
	m, ok := decoded.(map[string]any)
	if !ok {
		return Ref{}, fmt.Errorf("expected a stored or external artifact file reference object")
	}
	ref, err := parseFileRefMap(m)
	if err != nil {
		return Ref{}, fmt.Errorf("malformed artifact file reference: %w", err)
	}
	return ref, nil
}

func fileMembers(m map[string]any, allowed ...string) error {
	for k := range m {
		found := false
		for _, name := range allowed {
			found = found || k == name
		}
		if !found {
			return fmt.Errorf("unsupported member %q", k)
		}
	}
	return nil
}

func fileString(m map[string]any, key string) (string, error) {
	s, ok := m[key].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s must be a nonempty string", key)
	}
	return s, nil
}

func fileInteger(m map[string]any, key string) (int64, error) {
	n, ok := m[key].(json.Number)
	if ok {
		r, valid := new(big.Rat).SetString(string(n))
		if valid && r.IsInt() && r.Num().IsInt64() {
			return r.Num().Int64(), nil
		}
	}
	return 0, fmt.Errorf("%s must be an integer in the int64 range", key)
}

func parseFileRefMap(m map[string]any) (Ref, error) {
	kind, _ := m["kind"].(string)
	r := Ref{Kind: RefKind(kind)}
	if name, present := m["name"]; present {
		var ok bool
		r.Name, ok = name.(string)
		if !ok {
			return r, fmt.Errorf("name must be a string")
		}
	}
	switch r.Kind {
	case RefKindStored:
		if err := fileMembers(m, "kind", "name", "stored"); err != nil {
			return r, err
		}
		s, _ := m["stored"].(map[string]any)
		if err := fileMembers(s, "key"); err != nil {
			return r, err
		}
		k, _ := s["key"].(map[string]any)
		if err := fileMembers(k, "jobId", "taskOrdinal", "name", "sizeBytes"); err != nil {
			return r, err
		}
		key := jobdb.ArtifactKey{}
		var err error
		if key.JobId, err = fileString(k, "jobId"); err != nil {
			return r, err
		}
		if key.Name, err = fileString(k, "name"); err != nil {
			return r, err
		}
		if key.TaskOrdinal, err = fileInteger(k, "taskOrdinal"); err != nil {
			return r, err
		}
		if key.SizeBytes, err = fileInteger(k, "sizeBytes"); err != nil {
			return r, err
		}
		r.Stored = &StoredRef{Key: key}
	case RefKindExternal:
		if err := fileMembers(m, "kind", "name", "external"); err != nil {
			return r, err
		}
		if _, err := fileString(m, "name"); err != nil {
			return r, err
		}
		e, _ := m["external"].(map[string]any)
		if err := fileMembers(e, "url", "expand"); err != nil {
			return r, err
		}
		u, err := fileString(e, "url")
		if err != nil {
			return r, err
		}
		if expand, present := e["expand"]; present && expand != false {
			return r, fmt.Errorf("expand must be false or omitted")
		}
		if _, err := FileRefURL(u); err != nil {
			return r, err
		}
		r.External = &ExternalRef{URL: u}
	default:
		return r, fmt.Errorf("only stored and external artifact file references are supported")
	}
	return r, r.Validate()
}

// FileRefURL validates the source, without opening it or fetching any bytes.
func FileRefURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid external URL: %w", err)
	}
	switch u.Scheme {
	case "file":
		if u.Opaque != "" || (u.Host != "" && u.Host != "localhost") || u.User != nil || !filepath.IsAbs(u.Path) || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
			return nil, fmt.Errorf("file URL requires an absolute local file, empty/localhost host and no query or fragment")
		}
	case "http", "https":
		if u.Hostname() == "" || u.Opaque != "" {
			return nil, fmt.Errorf("HTTP(S) URL requires a host")
		}
	default:
		return nil, fmt.Errorf("unsupported external artifact file scheme %q (use file, http or https)", u.Scheme)
	}
	return u, nil
}
