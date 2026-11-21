package execution

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const BaseCatalogKey = "_c2j_execution_bases"
const BaseCatalogMaxBytes = 64 << 10

type CatalogScope struct {
	Runtime string `json:"runtime"`
	Tenant  string `json:"tenant"`
}
type DynamicBaseRule struct {
	Kind string `json:"kind"`
}

// BaseCatalog declares offered support, including busy hosts, never capacity.
type BaseCatalog struct {
	Version     int               `json:"version"`
	Scope       CatalogScope      `json:"scope"`
	Complete    bool              `json:"complete"`
	NamedBases  []string          `json:"named_bases"`
	Dynamic     []DynamicBaseRule `json:"dynamic"`
	Generation  string            `json:"generation"`
	PublishedAt time.Time         `json:"published_at"`
	ExpiresAt   time.Time         `json:"expires_at"`
}

// DecodeBaseCatalog rejects ambiguous JSON, including duplicate nested members.
func DecodeBaseCatalog(raw []byte, scope CatalogScope, now time.Time) (BaseCatalog, error) {
	var c BaseCatalog
	if len(raw) == 0 || len(raw) > BaseCatalogMaxBytes || !utf8.Valid(raw) {
		return c, fmt.Errorf("catalog absent or exceeds configuration value limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueCatalogJSON(d, "catalog"); err != nil {
		return c, err
	}
	if _, err := d.Token(); err != io.EOF {
		return c, fmt.Errorf("catalog must contain one JSON object")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid catalog schema")
	}
	if err := c.Validate(scope, now); err != nil {
		return c, err
	}
	slices.Sort(c.NamedBases)
	c.PublishedAt = c.PublishedAt.UTC()
	c.ExpiresAt = c.ExpiresAt.UTC()
	return c, nil
}
func uniqueCatalogJSON(d *json.Decoder, path string) error {
	t, err := d.Token()
	if err != nil {
		return fmt.Errorf("invalid catalog JSON")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		var allowed []string
		switch path {
		case "catalog":
			allowed = []string{"version", "scope", "complete", "named_bases", "dynamic", "generation", "published_at", "expires_at"}
		case "catalog.scope":
			allowed = []string{"runtime", "tenant"}
		case "catalog.dynamic[]":
			allowed = []string{"kind"}
		}
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			k, ok := key.(string)
			if !ok || seen[k] {
				return fmt.Errorf("duplicate catalog member")
			}
			// encoding/json also accepts case-insensitive struct field names.
			// Reject those aliases so differently cased duplicate fields cannot
			// silently replace a declared schema member.
			if !slices.Contains(allowed, k) {
				return fmt.Errorf("unknown catalog member")
			}
			seen[k] = true
			if err := uniqueCatalogJSON(d, path+"."+k); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueCatalogJSON(d, path+"[]"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("invalid catalog JSON")
	}
	_, err = d.Token()
	return err
}
func (c BaseCatalog) Validate(scope CatalogScope, now time.Time) error {
	if c.Version != 1 || !c.Complete || c.NamedBases == nil || c.Dynamic == nil || strings.TrimSpace(c.Generation) == "" {
		return fmt.Errorf("catalog requires version 1, complete=true, explicit named_bases/dynamic arrays and generation")
	}
	if scope.Runtime == "" || scope.Tenant == "" || c.Scope != scope {
		return fmt.Errorf("catalog scope mismatch")
	}
	_, publishedOffset := c.PublishedAt.Zone()
	_, expiryOffset := c.ExpiresAt.Zone()
	if c.PublishedAt.IsZero() || c.ExpiresAt.IsZero() || publishedOffset != 0 || expiryOffset != 0 || c.PublishedAt.After(now) || !c.ExpiresAt.After(now) || !c.ExpiresAt.After(c.PublishedAt) {
		return fmt.Errorf("catalog validity interval is future, expired or invalid; UTC required")
	}
	names := map[string]bool{}
	for _, name := range c.NamedBases {
		if err := validateBase(name, true); err != nil {
			return err
		}
		if names[name] {
			return fmt.Errorf("duplicate named base")
		}
		names[name] = true
	}
	rules := map[string]bool{}
	for _, rule := range c.Dynamic {
		if rule.Kind != "oci" || rules[rule.Kind] {
			return fmt.Errorf("unknown or duplicate dynamic namespace rule")
		}
		rules[rule.Kind] = true
	}
	return nil
}

type BaseDecision string

const (
	BaseAllowed      BaseDecision = "allowed"
	BaseUnsupported  BaseDecision = "unsupported"
	BaseCannotVerify BaseDecision = "cannot_verify"
)

// AdmitBase uses the existing whole-name '*' language, without aliases.
func (c BaseCatalog) AdmitBase(selector string) BaseDecision {
	if selector == "" || selector == "*" {
		return BaseAllowed
	}
	for _, name := range c.NamedBases {
		if MatchBase(selector, name) {
			return BaseAllowed
		}
	}
	for _, rule := range c.Dynamic {
		if rule.Kind != "oci" {
			continue
		}
		if !strings.Contains(selector, "*") {
			if strings.HasPrefix(selector, "oci:") && len(selector) > 4 {
				return BaseAllowed
			}
			continue
		}
		if globIntersectsPrefix(selector, "oci:") {
			return BaseCannotVerify
		}
	}
	return BaseUnsupported
}

// An NFA over a four-byte namespace prefix bounds work by selector length.
// Once the prefix is consumed every remaining literal can be a namespace suffix.
func globIntersectsPrefix(glob, prefix string) bool {
	states := make([]bool, len(glob)+1)
	states[0] = true
	close := func() {
		for i := 0; i < len(glob); i++ {
			if states[i] && glob[i] == '*' {
				states[i+1] = true
			}
		}
	}
	close()
	for j := 0; j < len(prefix); j++ {
		next := make([]bool, len(states))
		for i := 0; i < len(glob); i++ {
			if states[i] {
				if glob[i] == '*' {
					next[i] = true
				} else if glob[i] == prefix[j] {
					next[i+1] = true
				}
			}
		}
		states = next
		close()
	}
	for _, state := range states {
		if state {
			return true
		}
	}
	return false
}
