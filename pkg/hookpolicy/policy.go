// Package hookpolicy implements per-hook tenant defaults and repository overrides.
// Configuration is administrative data; recipes can request but cannot create grants.
package hookpolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/jobdb/pkg/jobdb"
	"gopkg.in/yaml.v3"
)

const TenantKey = "_c2j_hook_policy_tenant"
const CellPrefix = "_c2j_hook_policy_cell_"

type Trust struct {
	Mode   string `json:"mode"`
	Commit string `json:"commit,omitempty"`
}
type SourceTrust struct {
	Secrets map[string]Trust `json:"jobdb.secrets.resolve,omitempty"`
	Submit  *Trust           `json:"jobdb.jobs.submit,omitempty"`
}
type Entry struct {
	Perms permissions.Spec `json:"perms"`
	Trust SourceTrust      `json:"source_trust,omitempty"`
}
type Document struct {
	Version    int              `json:"version"`
	Repository string           `json:"repository,omitempty"`
	Hooks      map[string]Entry `json:"hooks"`
}

// Snapshot records absence as well as values; it is safe to persist in history.
type Snapshot struct {
	Revision   string    `json:"revision"`
	Repository string    `json:"repository"`
	Tenant     *Document `json:"tenant,omitempty"`
	Cell       *Document `json:"cell,omitempty"`
}

func Path(s string) (string, error) {
	if s == "" || path.Clean(s) != s || strings.HasPrefix(s, "/") || s == "." || s == ".." || strings.HasPrefix(s, "../") || strings.ContainsAny(s, "\\\x00\r\n") || strings.Contains(s, ":") {
		return "", fmt.Errorf("hook must be a normalized repository-relative path")
	}
	return s, nil
}
func Reference(s string) (string, error) {
	if !strings.HasPrefix(s, "cell://") {
		return "", fmt.Errorf("not a cell hook reference")
	}
	return Path(strings.TrimPrefix(s, "cell://"))
}
func CellKey(repository string) string {
	return fmt.Sprintf("%s%x", CellPrefix, sha256.Sum256([]byte(repository)))
}
func Identity(repository string) (string, error) {
	id := permissions.RepositoryIdentity(repository)
	if id == "" {
		return "", fmt.Errorf("cannot establish repository identity for %q", repository)
	}
	return id, nil
}

// Decode rejects unknown, null and duplicate fields, including within perms.
func Decode(raw []byte, out any) error {
	var n yaml.Node
	if err := yaml.Unmarshal(raw, &n); err != nil {
		return err
	}
	var check func(*yaml.Node) error
	check = func(n *yaml.Node) error {
		if n.Tag == "!!null" || n.Kind == yaml.AliasNode {
			return fmt.Errorf("nulls and aliases are not allowed in hook policy")
		}
		if n.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(n.Content); i += 2 {
				k := n.Content[i].Value
				if seen[k] {
					return fmt.Errorf("duplicate policy field %q", k)
				}
				seen[k] = true
			}
		}
		for _, c := range n.Content {
			if err := check(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(&n); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON policy document")
	}
	return nil
}

var hash = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (t Trust) Validate() error {
	if t.Mode == "candidate" && t.Commit == "" {
		return nil
	}
	if t.Mode == "commit" && hash.MatchString(t.Commit) {
		return nil
	}
	return fmt.Errorf("source trust requires candidate mode or commit mode with a full lowercase commit SHA")
}
func (t Trust) Allows(commit string) bool {
	return t.Mode == "candidate" || (t.Mode == "commit" && t.Commit == commit)
}

func Empty() Entry { return Entry{Perms: permissions.Spec{JobDB: &permissions.JobDB{}}} }
func (e Entry) Validate() error {
	if err := e.Perms.Validate(); err != nil {
		return err
	}
	g := permissions.JobDB{}
	if e.Perms.JobDB != nil {
		g = *e.Perms.JobDB
	}
	// Validate using the recipe's actual permission ceiling semantics.
	if _, err := (permissions.State{Version: permissions.Version}).Enter(&e.Perms, false); err != nil {
		return err
	}
	for _, name := range g.Secrets.Resolve {
		t, ok := e.Trust.Secrets[name]
		if !ok {
			return fmt.Errorf("secret %q requires source_trust", name)
		}
		if err := t.Validate(); err != nil {
			return err
		}
	}
	for name := range e.Trust.Secrets {
		if !slices.Contains(g.Secrets.Resolve, name) {
			return fmt.Errorf("source_trust for ungranted secret %q", name)
		}
	}
	if g.Jobs.Submit != (e.Trust.Submit != nil) {
		return fmt.Errorf("job submission grant and source_trust must be specified together")
	}
	if e.Trust.Submit != nil {
		return e.Trust.Submit.Validate()
	}
	return nil
}
func (d Document) Validate(repository string) error {
	if d.Version != 1 || d.Repository != repository || d.Hooks == nil {
		return fmt.Errorf("invalid hook policy version, repository binding, or hooks map")
	}
	for p, e := range d.Hooks {
		if _, err := Path(p); err != nil {
			return err
		}
		if err := e.Validate(); err != nil {
			return fmt.Errorf("hook %q: %w", p, err)
		}
	}
	return nil
}
func Capture(s jobdb.ConfigurationSnapshot, repository string) (Snapshot, error) {
	out := Snapshot{Revision: s.Revision, Repository: repository}
	documents := map[string]**Document{TenantKey: &out.Tenant}
	if repository != "" {
		documents[CellKey(repository)] = &out.Cell
	}
	for key, dest := range documents {
		if raw, ok := s.Values[key]; ok {
			var d Document
			if err := Decode(raw, &d); err != nil {
				return out, fmt.Errorf("%s: %w", key, err)
			}
			expected := ""
			if key != TenantKey {
				expected = repository
			}
			if err := d.Validate(expected); err != nil {
				return out, fmt.Errorf("%s: %w", key, err)
			}
			*dest = &d
		}
	}
	return out, nil
}
func (s Snapshot) Select(hook string) (Entry, string) {
	if s.Cell != nil {
		if e, ok := s.Cell.Hooks[hook]; ok {
			return Clone(e), "cell"
		}
	}
	if s.Tenant != nil {
		if e, ok := s.Tenant.Hooks[hook]; ok {
			return Clone(e), "tenant"
		}
	}
	return Empty(), "none"
}
func Clone(e Entry) Entry {
	raw, _ := json.Marshal(e)
	var out Entry
	_ = json.Unmarshal(raw, &out)
	return out
}

// Authority bounds both driver input materialization and delegated operation access.
func (e Entry) Authority(commit string, parent permissions.State, call *permissions.Spec) (permissions.State, error) {
	if err := e.Validate(); err != nil {
		return permissions.State{}, err
	}
	g := permissions.JobDB{}
	if e.Perms.JobDB != nil {
		g = *e.Perms.JobDB
	}
	for _, n := range g.Secrets.Resolve {
		if !e.Trust.Secrets[n].Allows(commit) {
			return permissions.State{}, fmt.Errorf("hook secret %q is not authorized for candidate %s", n, commit)
		}
	}
	if g.Jobs.Submit && !e.Trust.Submit.Allows(commit) {
		return permissions.State{}, fmt.Errorf("hook job submission is not authorized for candidate %s", commit)
	}
	s := permissions.State{Version: permissions.Version, Grant: &g, Limit: &g, Inputs: &permissions.InputPolicy{Foreign: true, Allowed: slices.Clone(g.Secrets.Resolve)}}
	// Each ceiling must narrow, never replenish, authority. Recipe declarations
	// outside the hook are not the source of its administrative callback grant.
	limits := []*permissions.JobDB{parent.Limit}
	if e.Perms.Limit != nil {
		limits = append(limits, e.Perms.Limit.JobDB)
	}
	if call != nil {
		if err := call.Validate(); err != nil {
			return s, err
		}
		limits = append(limits, call.JobDB)
		if call.Limit != nil {
			limits = append(limits, call.Limit.JobDB)
		}
	}
	for _, l := range limits {
		if l == nil {
			continue
		}
		g = Intersect(g, *l)
	}
	s.Grant = &g
	s.Limit = &g
	s.Inputs.Allowed = slices.Clone(g.Secrets.Resolve)
	// Commit-bound grants authorize this recorded invocation, not arbitrary
	// child recipe bodies or candidates supplied by it.
	child := permissions.JobDB{}
	for _, name := range g.Secrets.Resolve {
		if e.Trust.Secrets[name].Mode == "candidate" {
			child.Secrets.Resolve = append(child.Secrets.Resolve, name)
		}
	}
	child.Jobs.Submit = g.Jobs.Submit && e.Trust.Submit.Mode == "candidate"
	s.ChildGrant = &child
	return s, nil
}
func Intersect(a, b permissions.JobDB) permissions.JobDB {
	out := permissions.JobDB{Jobs: permissions.JobPermissions{Submit: a.Jobs.Submit && b.Jobs.Submit}}
	for _, n := range a.Secrets.Resolve {
		if slices.Contains(b.Secrets.Resolve, n) {
			out.Secrets.Resolve = append(out.Secrets.Resolve, n)
		}
	}
	return out
}

// Edit materializes inheritance for cell edits. An empty override remains explicit.
func Edit(s Snapshot, cell bool, hook, operation string, names []string, submit bool, trust Trust, replace bool, entry *Entry) (Document, error) {
	if _, err := Path(hook); err != nil {
		return Document{}, err
	}
	d := Document{Version: 1, Hooks: map[string]Entry{}}
	base := s.Tenant
	if cell {
		d.Repository = s.Repository
		base = s.Cell
	}
	if base != nil {
		for p, e := range base.Hooks {
			d.Hooks[p] = Clone(e)
		}
	}
	e := Empty()
	if cell {
		e, _ = s.Select(hook)
	} else if old, ok := d.Hooks[hook]; ok {
		e = Clone(old)
	}
	if e.Perms.JobDB == nil {
		e.Perms.JobDB = &permissions.JobDB{}
	}
	if e.Trust.Secrets == nil {
		e.Trust.Secrets = map[string]Trust{}
	}
	switch operation {
	case "inherit":
		if !cell {
			return d, fmt.Errorf("inherit requires --cell")
		}
		delete(d.Hooks, hook)
		return d, nil
	case "clear":
		e = Empty()
	case "set":
		if entry == nil {
			return d, fmt.Errorf("policy entry is required")
		}
		e = Clone(*entry)
	case "add", "remove":
		if len(names) == 0 && !submit {
			return d, fmt.Errorf("select --secret or --submit-jobs")
		}
		if operation == "add" {
			if err := trust.Validate(); err != nil {
				return d, err
			}
		}
		for _, n := range names {
			if operation == "remove" {
				e.Perms.JobDB.Secrets.Resolve = slices.DeleteFunc(e.Perms.JobDB.Secrets.Resolve, func(v string) bool { return v == n })
				delete(e.Trust.Secrets, n)
				continue
			}
			if old, ok := e.Trust.Secrets[n]; ok && old != trust && !replace {
				return d, fmt.Errorf("changing trust requires --replace")
			}
			if !slices.Contains(e.Perms.JobDB.Secrets.Resolve, n) {
				e.Perms.JobDB.Secrets.Resolve = append(e.Perms.JobDB.Secrets.Resolve, n)
			}
			e.Trust.Secrets[n] = trust
		}
		if submit {
			if operation == "remove" {
				e.Perms.JobDB.Jobs.Submit = false
				e.Trust.Submit = nil
			} else {
				if e.Trust.Submit != nil && *e.Trust.Submit != trust && !replace {
					return d, fmt.Errorf("changing trust requires --replace")
				}
				e.Perms.JobDB.Jobs.Submit = true
				e.Trust.Submit = &trust
			}
		}
	default:
		return d, fmt.Errorf("unknown policy edit %q", operation)
	}
	if e.Perms.JobDB != nil {
		slices.Sort(e.Perms.JobDB.Secrets.Resolve)
	}
	if err := e.Validate(); err != nil {
		return d, err
	}
	d.Hooks[hook] = e
	return d, nil
}
