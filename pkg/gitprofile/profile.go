// Package gitprofile implements tenant-backed Git routing and credentials.
package gitprofile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/colony-2/c2j/pkg/configuration"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

const (
	ProfilePrefix = "_c2j_git_profile_"
	SecretPrefix  = "_c2j_git_secret_"
	ReadOnlyKey   = "git-profile-ro"
	ReadWriteKey  = "git-profile-rw"
)

var namePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,127}$`)
var secretPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,255}$`)

type Profile struct {
	Version int     `json:"version" yaml:"version"`
	Entries []Entry `json:"entries" yaml:"entries"`
}
type Entry struct {
	URL         string   `json:"url" yaml:"url"`
	RewriteFrom []string `json:"rewrite_from,omitempty" yaml:"rewrite_from,omitempty"`
	Username    string   `json:"username,omitempty" yaml:"username,omitempty"`
	SecretRef   string   `json:"secret_ref,omitempty" yaml:"secret_ref,omitempty"`
	SecretFile  string   `json:"secret_file,omitempty" yaml:"secret_file,omitempty"`
}

func Key(name string) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("invalid Git profile name")
	}
	return ProfilePrefix + name, nil
}
func ProtocolValue(s string) bool { return !strings.ContainsAny(s, "\r\n\x00") && len(s) <= 65000 }

func Decode(data []byte, provisioning bool) (Profile, error) {
	var p Profile
	d := yaml.NewDecoder(bytes.NewReader(data))
	d.KnownFields(true)
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid Git profile document")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return p, fmt.Errorf("expected one Git profile document")
	}
	return p, p.Validate(provisioning)
}
func endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !ProtocolValue(raw) || strings.ContainsAny(raw, "\\\t ") {
		return nil, fmt.Errorf("Git destination must be an HTTPS URL without credentials, query, or fragment")
	}
	if strings.ContainsAny(u.Path, "\r\n\x00\\") || (u.Path != "" && path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") && u.Path != "/") {
		return nil, fmt.Errorf("invalid Git destination path")
	}
	return u, nil
}
func authority(u *url.URL) string {
	p := u.Port()
	if p == "" {
		p = "443"
	}
	return strings.ToLower(u.Hostname()) + ":" + p
}
func pathMatches(scope, request string) bool {
	scope = strings.Trim(scope, "/")
	request = strings.TrimPrefix(request, "/")
	return scope == "" || request == scope || strings.HasPrefix(request, scope+"/")
}
func (p Profile) Validate(provisioning bool) error {
	if p.Version != 1 || len(p.Entries) == 0 || len(p.Entries) > 128 {
		return fmt.Errorf("Git profile requires version 1 and 1–128 entries")
	}
	destinations := map[string]bool{}
	sources := map[string]bool{}
	for _, e := range p.Entries {
		u, err := endpoint(e.URL)
		if err != nil {
			return err
		}
		key := authority(u) + "/" + strings.Trim(u.Path, "/")
		if destinations[key] {
			return fmt.Errorf("ambiguous Git destination scope")
		}
		destinations[key] = true
		if !ProtocolValue(e.Username) || (e.SecretRef != "" && !secretPattern.MatchString(e.SecretRef)) {
			return fmt.Errorf("invalid Git credential fields")
		}
		if e.SecretFile != "" && (!provisioning || e.SecretRef != "") {
			return fmt.Errorf("secret_file is provisioning-only and cannot accompany secret_ref")
		}
		if (e.Username != "") != (e.SecretRef != "" || e.SecretFile != "") {
			return fmt.Errorf("Git credentials require username and one secret source")
		}
		for _, s := range e.RewriteFrom {
			if s == "" || !ProtocolValue(s) || strings.ContainsAny(s, "\\\t ") {
				return fmt.Errorf("invalid Git rewrite source")
			}
			if strings.HasPrefix(s, "git@") && strings.Contains(s, ":") {
				// Git's SCP-style SSH spelling is allowed as a source only.
			} else if _, err := endpoint(s); err != nil {
				return fmt.Errorf("rewrite source must be HTTPS or git@host:path")
			}
			if sources[s] {
				return fmt.Errorf("ambiguous Git rewrite source")
			}
			sources[s] = true
		}
	}
	for source := range sources {
		for _, e := range p.Entries {
			if strings.HasPrefix(e.URL, source) || strings.HasPrefix(source, e.URL) {
				return fmt.Errorf("Git rewrite chains and cycles are unsupported")
			}
		}
	}
	return nil
}

func (p Profile) Match(protocol, host, requestPath, username string) (Entry, error) {
	decoded, err := url.PathUnescape(requestPath)
	if err != nil {
		return Entry{}, fmt.Errorf("invalid credential path")
	}
	requestPath = decoded
	u, err := endpoint(protocol + "://" + host + "/")
	if err != nil || u.Path != "/" {
		return Entry{}, fmt.Errorf("credential destination is not allowed")
	}
	if !ProtocolValue(requestPath) || strings.Contains(requestPath, "\\") {
		return Entry{}, fmt.Errorf("invalid credential path")
	}
	if clean := path.Clean("/" + requestPath); clean != "/"+strings.TrimSuffix(requestPath, "/") && requestPath != "" {
		return Entry{}, fmt.Errorf("invalid credential path")
	}
	var selected *Entry
	longest := -1
	for _, e := range p.Entries {
		d, _ := endpoint(e.URL)
		if authority(d) == authority(u) && pathMatches(d.Path, requestPath) && len(d.Path) > longest {
			copy := e
			selected = &copy
			longest = len(d.Path)
		}
	}
	if selected == nil || selected.SecretRef == "" || (username != "" && username != selected.Username) {
		return Entry{}, fmt.Errorf("credential destination is not allowed")
	}
	return *selected, nil
}

// Selection distinguishes inherited defaults from an explicit name or null.
type Selection struct {
	Explicit bool `json:"explicit,omitempty"`
	Value    any  `json:"value,omitempty"`
	Publish  bool `json:"publish,omitempty"`
}

func (s Selection) Resolve(snapshot jobdb.ConfigurationSnapshot) (string, error) {
	v := s.Value
	if !s.Explicit {
		key := ReadOnlyKey
		if s.Publish {
			key = ReadWriteKey
		}
		if raw := snapshot.Values[key]; raw != nil {
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("invalid Git profile default")
			}
		}
	} else if ref, ok, err := configuration.Parse(v); err != nil {
		return "", err
	} else if ok {
		raw, exists := snapshot.Values[ref.Name]
		if !exists {
			raw = ref.Default
		}
		v = nil
		if len(raw) != 0 {
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("invalid profile configuration")
			}
		}
	}
	if v == nil {
		return "", nil
	}
	name, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("git_profile must resolve to a profile name or null")
	}
	_, err := Key(name)
	return name, err
}
func Load(snapshot jobdb.ConfigurationSnapshot, name string) (Profile, error) {
	key, err := Key(name)
	if err != nil {
		return Profile{}, err
	}
	raw, exists := snapshot.Values[key]
	if !exists {
		return Profile{}, fmt.Errorf("Git profile %q does not exist", name)
	}
	return Decode(raw, false)
}
