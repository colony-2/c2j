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

// BaseCatalog declares simultaneous configurations, never free capacity.
type BaseCatalog struct {
	Version     int            `json:"version"`
	Scope       CatalogScope   `json:"scope"`
	Complete    bool           `json:"complete"`
	Offerings   []BaseOffering `json:"offerings"`
	Generation  string         `json:"generation"`
	PublishedAt time.Time      `json:"published_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
}
type BaseOffering struct {
	Base         string             `json:"base"`
	Name         string             `json:"name"`
	Capabilities []string           `json:"capabilities"`
	Resources    *OfferingResources `json:"resources"`
}
type OfferingResources struct {
	CPU    *ResourceRule `json:"cpu"`
	Memory *ResourceRule `json:"memory"`
}
type ResourceRule struct {
	State string  `json:"state"`
	Min   *string `json:"min,omitempty"`
	Max   *string `json:"max,omitempty"`
}

// DecodeBaseCatalogStructure permits expired records for administration only.
func DecodeBaseCatalogStructure(raw []byte, scope CatalogScope) (BaseCatalog, error) {
	var c BaseCatalog
	if len(raw) == 0 || len(raw) > BaseCatalogMaxBytes || !utf8.Valid(raw) {
		return c, fmt.Errorf("catalog absent, invalid UTF-8 or exceeds configuration value limit")
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
	return c.Canonicalize(scope)
}
func DecodeBaseCatalog(raw []byte, scope CatalogScope, now time.Time) (BaseCatalog, error) {
	c, err := DecodeBaseCatalogStructure(raw, scope)
	if err == nil {
		err = c.Validate(scope, now)
	}
	return c, err
}
func uniqueCatalogJSON(d *json.Decoder, path string) error {
	t, err := d.Token()
	if err != nil {
		return fmt.Errorf("invalid catalog JSON")
	}
	if t == nil && (strings.HasSuffix(path, ".min") || strings.HasSuffix(path, ".max")) {
		return fmt.Errorf("resource endpoints cannot be null")
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		allowed := map[string][]string{
			"catalog":                              {"version", "scope", "complete", "offerings", "generation", "published_at", "expires_at"},
			"catalog.scope":                        {"runtime", "tenant"},
			"catalog.offerings[]":                  {"base", "name", "capabilities", "resources"},
			"catalog.offerings[].resources":        {"cpu", "memory"},
			"catalog.offerings[].resources.cpu":    {"state", "min", "max"},
			"catalog.offerings[].resources.memory": {"state", "min", "max"},
		}[path]
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			k, ok := key.(string)
			if !ok || seen[k] {
				return fmt.Errorf("duplicate catalog member")
			}
			if !slices.Contains(allowed, k) {
				return fmt.Errorf("unknown catalog member %q", k)
			}
			seen[k] = true
			if e := uniqueCatalogJSON(d, path+"."+k); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := uniqueCatalogJSON(d, path+"[]"); e != nil {
				return e
			}
		}
	default:
		return fmt.Errorf("invalid catalog JSON")
	}
	_, err = d.Token()
	return err
}

// Canonicalize validates structure and returns independent, sorted declarations.
func (c BaseCatalog) Canonicalize(scope CatalogScope) (BaseCatalog, error) {
	if c.Version != 1 || !c.Complete || c.Offerings == nil || strings.TrimSpace(c.Generation) == "" {
		return c, fmt.Errorf("catalog requires version 1, complete=true, explicit offerings and generation")
	}
	if scope.Runtime == "" || scope.Tenant == "" || c.Scope != scope {
		return c, fmt.Errorf("catalog scope mismatch")
	}
	_, po := c.PublishedAt.Zone()
	_, eo := c.ExpiresAt.Zone()
	if c.PublishedAt.IsZero() || c.ExpiresAt.IsZero() || po != 0 || eo != 0 || !c.ExpiresAt.After(c.PublishedAt) {
		return c, fmt.Errorf("invalid catalog validity interval; UTC required")
	}
	c.Offerings = slices.Clone(c.Offerings)
	seen := map[string]bool{}
	for i, o := range c.Offerings {
		if err := ValidateNamedBase(o.Base); err != nil {
			return c, err
		}
		if !capabilityName.MatchString(o.Name) {
			return c, fmt.Errorf("invalid offering name %q", o.Name)
		}
		key := o.Base + "\x00" + o.Name
		if seen[key] {
			return c, fmt.Errorf("duplicate offering identity")
		}
		seen[key] = true
		if o.Capabilities == nil || o.Resources == nil {
			return c, fmt.Errorf("offering requires explicit capabilities and resources")
		}
		if err := validateCapabilities(o.Capabilities); err != nil {
			return c, err
		}
		o.Capabilities = slices.Clone(o.Capabilities)
		slices.Sort(o.Capabilities)
		for j := 1; j < len(o.Capabilities); j++ {
			if o.Capabilities[j] == o.Capabilities[j-1] {
				return c, fmt.Errorf("duplicate capability")
			}
		}
		cpu, err := normalizeRule(o.Resources.CPU, true)
		if err != nil {
			return c, fmt.Errorf("%s/%s CPU: %w", o.Base, o.Name, err)
		}
		memory, err := normalizeRule(o.Resources.Memory, false)
		if err != nil {
			return c, fmt.Errorf("%s/%s memory: %w", o.Base, o.Name, err)
		}
		o.Resources = &OfferingResources{CPU: cpu, Memory: memory}
		c.Offerings[i] = o
	}
	slices.SortFunc(c.Offerings, func(a, b BaseOffering) int {
		if n := strings.Compare(a.Base, b.Base); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if len(raw) > BaseCatalogMaxBytes {
		return c, fmt.Errorf("canonical catalog exceeds configuration value limit")
	}
	return c, nil
}
func ValidateNamedBase(name string) error {
	if err := validateBase(name, true); err != nil {
		return err
	}
	if strings.HasPrefix(name, "oci:") {
		return fmt.Errorf("OCI bases are not supported")
	}
	return nil
}
func ValidateCatalogSelector(selector string) error {
	if selector == "" {
		return nil
	}
	if err := validateBase(selector, false); err != nil {
		return err
	}
	if strings.HasPrefix(selector, "oci:") {
		return fmt.Errorf("OCI bases are not supported")
	}
	return nil
}
func normalizeRule(rule *ResourceRule, cpu bool) (*ResourceRule, error) {
	if rule == nil {
		return nil, fmt.Errorf("explicit resource state required")
	}
	r := *rule
	switch r.State {
	case "unbounded", "not_allowed":
		if r.Min != nil || r.Max != nil {
			return nil, fmt.Errorf("%s cannot have bounds", r.State)
		}
	case "range":
		if r.Min == nil && r.Max == nil {
			return nil, fmt.Errorf("range needs at least one endpoint")
		}
		var lo, hi int64
		for i, p := range []*string{r.Min, r.Max} {
			if p == nil {
				continue
			}
			_, n, err := parseQuantity(*p, cpu)
			if err != nil {
				return nil, err
			}
			var v string
			if cpu {
				v, err = normalizeCPU(*p)
			} else {
				v, err = normalizeBytes(*p)
			}
			if err != nil {
				return nil, err
			}
			if i == 0 {
				r.Min = &v
				lo = n
			} else {
				r.Max = &v
				hi = n
			}
		}
		if r.Min != nil && r.Max != nil && lo > hi {
			return nil, fmt.Errorf("minimum exceeds maximum")
		}
	default:
		return nil, fmt.Errorf("unknown resource state %q", r.State)
	}
	return &r, nil
}
func (c BaseCatalog) Validate(scope CatalogScope, now time.Time) error {
	if _, err := c.Canonicalize(scope); err != nil {
		return err
	}
	if c.PublishedAt.After(now) || !c.ExpiresAt.After(now) {
		return fmt.Errorf("catalog validity interval is future or expired")
	}
	return nil
}
func (c BaseCatalog) Names() []string {
	names := []string{}
	for _, o := range c.Offerings {
		if !slices.Contains(names, o.Base) {
			names = append(names, o.Base)
		}
	}
	slices.Sort(names)
	return names
}

type OfferingIdentity struct {
	Base string `json:"base"`
	Name string `json:"name"`
}
type CatalogReason struct {
	Offering  *OfferingIdentity `json:"offering,omitempty"`
	Field     string            `json:"field"`
	Requested string            `json:"requested,omitempty"`
	Allowed   any               `json:"allowed,omitempty"`
	Reason    string            `json:"reason"`
}
type CatalogEvaluation struct {
	Decision string             `json:"decision"`
	Request  *Requirements      `json:"request,omitempty"`
	Matches  []OfferingIdentity `json:"matches"`
	Reasons  []CatalogReason    `json:"reasons"`
}

func (c BaseCatalog) Evaluate(needs Requirements) CatalogEvaluation {
	e := CatalogEvaluation{Matches: []OfferingIdentity{}, Reasons: []CatalogReason{}}
	r, err := needs.Normalize()
	if err == nil && r.Base != nil {
		err = ValidateCatalogSelector(*r.Base)
	}
	if err != nil {
		e.Decision = "invalid_request"
		e.Reasons = append(e.Reasons, CatalogReason{Field: "request", Reason: err.Error()})
		return e
	}
	e.Request = &r
	selector := ""
	if r.Base != nil {
		selector = *r.Base
	}
	candidates := 0
	for _, o := range c.Offerings {
		if selector != "" && !MatchBase(selector, o.Base) {
			continue
		}
		candidates++
		id := OfferingIdentity{Base: o.Base, Name: o.Name}
		before := len(e.Reasons)
		for _, cap := range r.Capabilities {
			if !slices.Contains(o.Capabilities, cap) {
				e.Reasons = append(e.Reasons, CatalogReason{Offering: &id, Field: "capabilities", Requested: cap, Allowed: o.Capabilities, Reason: "capability is not offered"})
			}
		}
		for _, f := range []struct {
			name  string
			value *string
			rule  *ResourceRule
			cpu   bool
		}{{"cpu", r.Resources.CPU, o.Resources.CPU, true}, {"memory", r.Resources.Memory, o.Resources.Memory, false}} {
			if f.value == nil {
				continue
			}
			reason := ""
			if f.rule.State == "not_allowed" {
				reason = "explicit resource requests are not allowed"
			} else if f.rule.State == "range" {
				_, n, _ := parseQuantity(*f.value, f.cpu)
				if f.rule.Min != nil {
					_, lo, _ := parseQuantity(*f.rule.Min, f.cpu)
					if n < lo {
						reason = "request is below minimum"
					}
				}
				if f.rule.Max != nil {
					_, hi, _ := parseQuantity(*f.rule.Max, f.cpu)
					if n > hi {
						reason = "request exceeds maximum"
					}
				}
			}
			if reason != "" {
				e.Reasons = append(e.Reasons, CatalogReason{Offering: &id, Field: "resources." + f.name, Requested: *f.value, Allowed: f.rule, Reason: reason})
			}
		}
		if before == len(e.Reasons) {
			e.Matches = append(e.Matches, id)
		}
	}
	if len(e.Matches) > 0 {
		e.Decision = "allowed"
	} else {
		e.Decision = "not_allowed"
		if candidates == 0 {
			e.Reasons = append(e.Reasons, CatalogReason{Field: "base", Requested: selector, Allowed: c.Names(), Reason: "no matching offering"})
		}
	}
	return e
}
