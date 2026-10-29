// Package permissions defines recipe grants independently of JobDB's token format.
package permissions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

const Version = 1

// Spec is common node metadata. On ordinary nodes JobDB replaces the delegated
// grant; on includes it authorizes callee inputs and bounds callee grants.
type Spec struct {
	JobDB *JobDB `yaml:"jobdb,omitempty" json:"jobdb,omitempty"`
	Limit *Limit `yaml:"limit,omitempty" json:"limit,omitempty"`
}

type Limit struct {
	JobDB *JobDB `yaml:"jobdb,omitempty" json:"jobdb,omitempty"`
}

type JobDB struct {
	Secrets SecretPermissions `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Jobs    JobPermissions    `yaml:"jobs,omitempty" json:"jobs,omitempty"`
}

type SecretPermissions struct {
	Resolve []string `yaml:"resolve,omitempty" json:"resolve,omitempty"`
}

type JobPermissions struct {
	Submit bool `yaml:"submit,omitempty" json:"submit,omitempty"`
}

var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,255}$`)

func (p JobDB) Validate() error {
	if len(p.Secrets.Resolve) > 256 {
		return fmt.Errorf("perms.jobdb.secrets.resolve accepts at most 256 names")
	}
	seen := map[string]bool{}
	for _, name := range p.Secrets.Resolve {
		if !secretName.MatchString(name) || seen[name] {
			return fmt.Errorf("invalid or duplicate secret name in perms.jobdb.secrets.resolve")
		}
		seen[name] = true
	}
	return nil
}

func (p *Spec) Validate() error {
	if p == nil {
		return nil
	}
	if p.JobDB != nil {
		if err := p.JobDB.Validate(); err != nil {
			return err
		}
	}
	if p.Limit != nil && p.Limit.JobDB != nil {
		return p.Limit.JobDB.Validate()
	}
	return nil
}

// Reject unknown fields, nulls, aliases and merges rather than silently ignoring
// an intended restriction. This strictness is local to the permission subtree.
func (p *Spec) UnmarshalYAML(n *yaml.Node) error {
	shape := map[string]any{"jobdb": grantShape(), "limit": map[string]any{"jobdb": grantShape()}}
	if err := validateYAML(n, shape, "perms"); err != nil {
		return err
	}
	type plain Spec
	var out plain
	if err := n.Decode(&out); err != nil {
		return err
	}
	*p = Spec(out)
	return p.Validate()
}

func grantShape() map[string]any {
	return map[string]any{"secrets": map[string]any{"resolve": "names"}, "jobs": map[string]any{"submit": "bool"}}
}

func validateYAML(n *yaml.Node, shape any, path string) error {
	if fields, ok := shape.(map[string]any); ok {
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be a mapping", path)
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i].Value
			child, ok := fields[key]
			if !ok || seen[key] {
				return fmt.Errorf("unknown or duplicate permission field %s.%s", path, key)
			}
			seen[key] = true
			if err := validateYAML(n.Content[i+1], child, path+"."+key); err != nil {
				return err
			}
		}
		return nil
	}
	if shape == "bool" {
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
			return fmt.Errorf("%s must be a boolean", path)
		}
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s must be a list of secret names", path)
	}
	for _, v := range n.Content {
		if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
			return fmt.Errorf("%s must contain literal strings", path)
		}
	}
	return nil
}

func (Spec) JSONSchema() *jsonschema.Schema {
	object := func(fields map[string]*jsonschema.Schema) *jsonschema.Schema {
		p := jsonschema.NewProperties()
		keys := make([]string, 0, len(fields))
		for key := range fields {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			p.Set(key, fields[key])
		}
		return &jsonschema.Schema{Type: "object", Properties: p, AdditionalProperties: jsonschema.FalseSchema}
	}
	grant := object(map[string]*jsonschema.Schema{
		"secrets": object(map[string]*jsonschema.Schema{"resolve": {Type: "array", Items: &jsonschema.Schema{Type: "string", Pattern: secretName.String()}, UniqueItems: true, MaxItems: ptr(uint64(256))}}),
		"jobs":    object(map[string]*jsonschema.Schema{"submit": {Type: "boolean"}}),
	})
	return object(map[string]*jsonschema.Schema{"jobdb": grant, "limit": object(map[string]*jsonschema.Schema{"jobdb": grant})})
}

func ptr[T any](v T) *T { return &v }

// State is immutable lexical policy, serialized with each task invocation.
// Input authorization is independent of grants and automatic Git defaults.
type State struct {
	// ChildGrant excludes invocation-only grants from arbitrary child bodies.
	ChildGrant *JobDB `json:"child_grant,omitempty"`
	// ChildPolicy is controller-captured configuration. Its presence requires
	// child submission through the runtime path that installs the ceiling.
	ChildPolicy json.RawMessage `json:"child_policy,omitempty"`
	Version     int             `json:"version"`
	Grant       *JobDB          `json:"grant,omitempty"`
	Limit       *JobDB          `json:"limit,omitempty"`
	Inputs      *InputPolicy    `json:"inputs,omitempty"`
}

func (s State) Enter(spec *Spec, boundary bool) (State, error) {
	if boundary {
		return s.EnterInclude(spec, true, false)
	}
	s.Version = Version
	if err := spec.Validate(); err != nil {
		return State{}, err
	}
	if spec != nil {
		if spec.Limit != nil && spec.Limit.JobDB != nil {
			limit := clone(*spec.Limit.JobDB)
			if s.Limit != nil {
				limit = intersect(limit, *s.Limit)
			}
			s.Limit = &limit
		}
		if spec.JobDB != nil {
			v := clone(*spec.JobDB)
			s.Grant = &v
		}
	}
	if s.Grant != nil && s.Limit != nil && !subset(*s.Grant, *s.Limit) {
		return State{}, fmt.Errorf("requested JobDB permissions exceed the enclosing limit")
	}
	return s, nil
}

func (s State) Resolve(gitSecrets []string) (JobDB, error) {
	if s.Version != Version {
		return JobDB{}, fmt.Errorf("unsupported permission policy version %d", s.Version)
	}
	grant := JobDB{Secrets: SecretPermissions{Resolve: slices.Clone(gitSecrets)}, Jobs: JobPermissions{Submit: true}}
	if s.Grant != nil {
		grant = clone(*s.Grant)
	}
	slices.Sort(grant.Secrets.Resolve)
	grant.Secrets.Resolve = slices.Compact(grant.Secrets.Resolve)
	if err := grant.Validate(); err != nil {
		return JobDB{}, err
	}
	if s.Limit != nil {
		if err := s.Limit.Validate(); err != nil {
			return JobDB{}, err
		}
		if s.Grant != nil && !subset(grant, *s.Limit) {
			return JobDB{}, fmt.Errorf("requested JobDB permissions exceed the enclosing limit")
		}
		grant = intersect(grant, *s.Limit)
	}
	return grant, nil
}

func (s *State) UnmarshalJSON(raw []byte) error {
	type plain State
	var p plain
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid permission policy")
	}
	*s = State(p)
	_, err := s.Resolve(nil)
	return err
}

func clone(g JobDB) JobDB { g.Secrets.Resolve = slices.Clone(g.Secrets.Resolve); return g }
func intersect(a, b JobDB) JobDB {
	out := JobDB{Jobs: JobPermissions{Submit: a.Jobs.Submit && b.Jobs.Submit}}
	for _, name := range a.Secrets.Resolve {
		if slices.Contains(b.Secrets.Resolve, name) {
			out.Secrets.Resolve = append(out.Secrets.Resolve, name)
		}
	}
	return out
}
func subset(a, b JobDB) bool {
	if a.Jobs.Submit && !b.Jobs.Submit {
		return false
	}
	for _, name := range a.Secrets.Resolve {
		if !slices.Contains(b.Secrets.Resolve, name) {
			return false
		}
	}
	return true
}
