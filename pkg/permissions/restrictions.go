package permissions

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Restriction edits the current grant. A replacement grant discards inherited
// edits, unlike a Limit. Pointers preserve explicit false across serialization.
type Restriction struct {
	JobDB *JobDBRestriction `yaml:"jobdb,omitempty" json:"jobdb,omitempty"`
}

type JobDBRestriction struct {
	Jobs  RestrictedJobs  `yaml:"jobs,omitempty" json:"jobs,omitzero"`
	Cells RestrictedCells `yaml:"cells,omitempty" json:"cells,omitzero"`
}

type RestrictedJobs struct {
	Submit *bool `yaml:"submit,omitempty" json:"submit,omitempty"`
}

type RestrictedCells struct {
	Create *bool `yaml:"create,omitempty" json:"create,omitempty"`
}

// Recorded invocations use JSON. Preserve the recipe subtree's strictness,
// particularly null versus an omitted or explicit false field.
func (r *Restriction) UnmarshalJSON(raw []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return err
	}
	if err := validateYAML(node.Content[0], restrictionShape(), "perms.restrict"); err != nil {
		return err
	}
	type plain Restriction
	var out plain
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*r = Restriction(out)
	return r.Validate()
}

func (r *Restriction) Validate() error {
	if r == nil || r.JobDB == nil {
		return nil
	}
	for name, value := range map[string]*bool{"jobs.submit": r.JobDB.Jobs.Submit, "cells.create": r.JobDB.Cells.Create} {
		if value != nil && *value {
			return fmt.Errorf("perms.restrict.jobdb.%s only accepts false; use perms.jobdb to replace the grant", name)
		}
	}
	return nil
}

func restrictionShape() map[string]any {
	return map[string]any{"jobdb": map[string]any{"jobs": map[string]any{"submit": "bool"}, "cells": map[string]any{"create": "bool"}}}
}

// mergeRestrictions always allocates: entering a child must not edit siblings.
func mergeRestrictions(a, b *Restriction) *Restriction {
	var out JobDBRestriction
	for _, r := range []*Restriction{a, b} {
		if r == nil || r.JobDB == nil {
			continue
		}
		if v := r.JobDB.Jobs.Submit; v != nil {
			out.Jobs.Submit = ptr(*v)
		}
		if v := r.JobDB.Cells.Create; v != nil {
			out.Cells.Create = ptr(*v)
		}
	}
	if out.Jobs.Submit == nil && out.Cells.Create == nil {
		return nil
	}
	return &Restriction{JobDB: &out}
}

func (r *Restriction) apply(g JobDB) JobDB {
	g = clone(g)
	if r != nil && r.JobDB != nil {
		if r.JobDB.Jobs.Submit != nil {
			g.Jobs.Submit = false
			g.Jobs.CellNames = nil
		}
		if r.JobDB.Cells.Create != nil {
			g.Cells.Create = false
			g.Cells.CellNames = nil
		}
	}
	return g
}

// Fields returns active edits for inspection without exposing credentials.
func (r *Restriction) Fields() []string {
	var fields []string
	if r != nil && r.JobDB != nil {
		if r.JobDB.Jobs.Submit != nil {
			fields = append(fields, "perms.restrict.jobdb.jobs.submit")
		}
		if r.JobDB.Cells.Create != nil {
			fields = append(fields, "perms.restrict.jobdb.cells.create")
		}
	}
	return fields
}
