package permissions

import (
	"encoding/json"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"
)

// JobDBOverrides preserves field presence for perms.inherit. Omitted fields
// inherit, explicit booleans replace, and explicit lists replace (never union).
type JobDBOverrides struct {
	Secrets SecretOverrides `yaml:"secrets,omitempty" json:"secrets,omitzero"`
	Jobs    JobOverrides    `yaml:"jobs,omitempty" json:"jobs,omitzero"`
	Cells   CellOverrides   `yaml:"cells,omitempty" json:"cells,omitzero"`
}

type SecretOverrides struct {
	Resolve *[]string `yaml:"resolve,omitempty" json:"resolve,omitempty"`
}
type JobOverrides struct {
	Submit    *bool     `yaml:"submit,omitempty" json:"submit,omitempty"`
	CellNames *[]string `yaml:"cellNames,omitempty" json:"cellNames,omitempty"`
}
type CellOverrides struct {
	Create    *bool     `yaml:"create,omitempty" json:"create,omitempty"`
	CellNames *[]string `yaml:"cellNames,omitempty" json:"cellNames,omitempty"`
}

func (p JobDBOverrides) Validate() error {
	// An omitted action may be supplied by the inherited grant. Validate that
	// dependency after merging; literal names and explicit conflicts are checked now.
	g := JobDB{Jobs: JobPermissions{Submit: true}, Cells: CellPermissions{Create: true}}
	g, err := p.apply(g)
	if err != nil {
		return err
	}
	return g.Validate()
}

func (p JobDBOverrides) apply(g JobDB) (JobDB, error) {
	g = clone(g)
	if p.Secrets.Resolve != nil {
		if *p.Secrets.Resolve == nil {
			return JobDB{}, fmt.Errorf("secrets.resolve must be a list, not null")
		}
		g.Secrets.Resolve = slices.Clone(*p.Secrets.Resolve)
	}
	if p.Jobs.Submit != nil {
		g.Jobs.Submit = *p.Jobs.Submit
		if !g.Jobs.Submit {
			g.Jobs.CellNames = nil
		}
	}
	if p.Jobs.CellNames != nil {
		if *p.Jobs.CellNames == nil {
			return JobDB{}, fmt.Errorf("jobs.cellNames must be a list, not null")
		}
		g.Jobs.CellNames = slices.Clone(*p.Jobs.CellNames)
	}
	if p.Cells.Create != nil {
		g.Cells.Create = *p.Cells.Create
		if !g.Cells.Create {
			g.Cells.CellNames = nil
		}
	}
	if p.Cells.CellNames != nil {
		if *p.Cells.CellNames == nil {
			return JobDB{}, fmt.Errorf("cells.cellNames must be a list, not null")
		}
		g.Cells.CellNames = slices.Clone(*p.Cells.CellNames)
	}
	return g, nil
}

func (p JobDBOverrides) clone() JobDBOverrides {
	copyBool := func(v *bool) *bool {
		if v == nil {
			return nil
		}
		return ptr(*v)
	}
	copyNames := func(v *[]string) *[]string {
		if v == nil {
			return nil
		}
		return ptr(slices.Clone(*v))
	}
	return JobDBOverrides{
		Secrets: SecretOverrides{Resolve: copyNames(p.Secrets.Resolve)},
		Jobs:    JobOverrides{Submit: copyBool(p.Jobs.Submit), CellNames: copyNames(p.Jobs.CellNames)},
		Cells:   CellOverrides{Create: copyBool(p.Cells.Create), CellNames: copyNames(p.Cells.CellNames)},
	}
}

func (p JobDBOverrides) empty() bool {
	return p.Secrets.Resolve == nil && p.Jobs.Submit == nil && p.Jobs.CellNames == nil && p.Cells.Create == nil && p.Cells.CellNames == nil
}

// Keep recorded overrides as strict as recipe YAML. In particular, null must
// never silently erase an explicit false or empty list during resume.
func (p *JobDBOverrides) UnmarshalJSON(raw []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return err
	}
	if err := validateYAML(node.Content[0], grantShape(), "perms.jobdb"); err != nil {
		return err
	}
	type plain JobDBOverrides
	var out plain
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*p = JobDBOverrides(out)
	return p.Validate()
}

// The same public jobdb key holds a complete grant or presence-aware overrides.
// Go callers use Overrides with Inherit, and JobDB for a replacement grant.
type specWire struct {
	Inherit bool   `yaml:"inherit,omitempty" json:"inherit,omitempty"`
	JobDB   any    `yaml:"jobdb,omitempty" json:"jobdb,omitempty"`
	Limit   *Limit `yaml:"limit,omitempty" json:"limit,omitempty"`
}

func (p Spec) wire() (specWire, error) {
	if err := p.Validate(); err != nil {
		return specWire{}, err
	}
	w := specWire{Inherit: p.Inherit, Limit: p.Limit}
	if p.Inherit && p.Overrides != nil {
		w.JobDB = p.Overrides
	}
	if !p.Inherit && p.JobDB != nil {
		w.JobDB = p.JobDB
	}
	return w, nil
}
func (p Spec) MarshalYAML() (any, error) { return p.wire() }
func (p Spec) MarshalJSON() ([]byte, error) {
	w, err := p.wire()
	if err != nil {
		return nil, err
	}
	return json.Marshal(w)
}
func (p *Spec) UnmarshalJSON(raw []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return err
	}
	return p.UnmarshalYAML(node.Content[0])
}

// InheritedDenials identifies explicit inherited overrides that currently
// disable a capability. A later true/list override removes the earlier denial.
func (s State) InheritedDenials() []string {
	var submit, create, secrets *bool
	for _, p := range s.Overrides {
		if p.Jobs.Submit != nil {
			submit = p.Jobs.Submit
		}
		if p.Cells.Create != nil {
			create = p.Cells.Create
		}
		if p.Secrets.Resolve != nil {
			secrets = ptr(len(*p.Secrets.Resolve) > 0)
		}
	}
	var fields []string
	for _, field := range []struct {
		name  string
		value *bool
	}{{"jobs.submit", submit}, {"cells.create", create}, {"secrets.resolve", secrets}} {
		if field.value != nil && !*field.value {
			fields = append(fields, "perms.jobdb."+field.name)
		}
	}
	return fields
}
