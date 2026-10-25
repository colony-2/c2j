package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// DemandSchemaVersion identifies the scheduling snapshot format.
const DemandSchemaVersion = 3

// Demand is a disposable scheduling snapshot, never replay state.
type Demand struct {
	SchemaVersion    int          `json:"schema_version"`
	RecipeResolution string       `json:"recipe_resolution"`
	Effective        Requirements `json:"effective"`
}

// Submission retains the original overrides independently of the initial
// scheduling hint. Replay always starts with these overrides and the recipe.
type Submission struct {
	Demand
	JobRequirements Requirements `json:"job_requirements"`
}

func Initial(base *Requirements, resolved bool, overrides Requirements) (Submission, error) {
	d := Submission{Demand: Demand{SchemaVersion: DemandSchemaVersion, RecipeResolution: "unresolved"}}
	if resolved {
		d.RecipeResolution = "resolved"
	}
	var err error
	d.JobRequirements, err = overrides.Normalize()
	if err != nil {
		return Submission{}, err
	}
	b := Requirements{}
	if base != nil {
		b, err = base.Normalize()
		if err != nil {
			return Submission{}, err
		}
	}
	d.Effective = Overlay(b, d.JobRequirements)
	return d, nil
}

type UnsupportedVersionError struct{ Version int }

func (e *UnsupportedVersionError) Error() string {
	return fmt.Sprintf("unsupported execution schema version %d", e.Version)
}
func (d Demand) Validate() error {
	if d.SchemaVersion != DemandSchemaVersion {
		return &UnsupportedVersionError{d.SchemaVersion}
	}
	if d.RecipeResolution != "resolved" && d.RecipeResolution != "unresolved" {
		return fmt.Errorf("invalid recipe resolution %q", d.RecipeResolution)
	}
	_, err := d.Effective.Normalize()
	return err
}
func (s Submission) Validate() error {
	if err := s.Demand.Validate(); err != nil {
		return err
	}
	_, err := s.JobRequirements.Normalize()
	return err
}

// PayloadDemand interprets only the reserved client-owned namespace. Other
// namespaces remain opaque, including JSON numbers not representable as floats.
func PayloadDemand(raw json.RawMessage) (*Demand, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("execution client payload must be an object: %w", err)
	}
	if len(root["c2j"]) == 0 {
		return nil, nil
	}
	var ns map[string]json.RawMessage
	if err := json.Unmarshal(root["c2j"], &ns); err != nil {
		return nil, fmt.Errorf("invalid c2j client namespace: %w", err)
	}
	if len(ns["execution"]) == 0 {
		return nil, nil
	}
	return DecodeDemand(ns["execution"])
}

func DecodeDemand(raw json.RawMessage) (*Demand, error) {
	var d Demand
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// PayloadWithDemand replaces just the execution snapshot, preserving all other
// client JSON. The caller publishes it with an observed-revision checked reset.
func PayloadWithDemand(raw json.RawMessage, d Demand) (json.RawMessage, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	root := map[string]json.RawMessage{}
	if len(raw) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &root); err != nil {
			return nil, err
		}
	}
	ns := map[string]json.RawMessage{}
	if v := root["c2j"]; len(v) > 0 {
		if err := json.Unmarshal(v, &ns); err != nil {
			return nil, err
		}
	}
	if ns == nil {
		ns = map[string]json.RawMessage{}
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	ns["execution"] = encoded
	root["c2j"], err = json.Marshal(ns)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

type View struct {
	Status     string      `json:"status"`
	Source     string      `json:"source"`
	Published  bool        `json:"published"`
	Demand     *Demand     `json:"demand,omitempty"`
	Initial    *Submission `json:"initial,omitempty"`
	Diagnostic string      `json:"diagnostic,omitempty"`
}

// Inspect never resolves recipes or reads history. Missing metadata remains
// unknown, even if a compatible executor has since resolved the recipe.
func Inspect(metadata, payload json.RawMessage) View {
	v := View{Status: "unresolved", Source: "absent"}
	var meta map[string]json.RawMessage
	var err error
	if len(metadata) > 0 {
		err = json.Unmarshal(metadata, &meta)
	}
	if err == nil && len(meta["execution"]) > 0 {
		var initial Submission
		dec := json.NewDecoder(bytes.NewReader(meta["execution"]))
		dec.DisallowUnknownFields()
		err = dec.Decode(&initial)
		if err == nil {
			err = initial.Validate()
		}
		if err == nil {
			v.Initial = &initial
		}
	}
	if err != nil {
		v.Status, v.Diagnostic = demandErrorStatus(err), err.Error()
		return v
	}
	v.Demand, err = PayloadDemand(payload)
	if err != nil {
		v.Status, v.Source, v.Diagnostic = demandErrorStatus(err), "yield", err.Error()
		return v
	}
	if v.Demand != nil {
		v.Source, v.Published = "yield", true
	} else if v.Initial != nil {
		v.Demand, v.Source = &v.Initial.Demand, "submission"
	}
	if v.Demand != nil && v.Demand.RecipeResolution == "resolved" {
		v.Status = "specified"
		if v.Demand.Effective.Empty() {
			v.Status = "unspecified"
		}
	}
	return v
}

func demandErrorStatus(err error) string {
	var version *UnsupportedVersionError
	if errors.As(err, &version) {
		return "unsupported"
	}
	return "malformed"
}

type Filter struct {
	Allocation        Allocation `json:"allocation"`
	IncludeUnresolved bool       `json:"include_unresolved"`
}

func (f Filter) Match(v View) (bool, error) {
	if v.Status == "in_flight" || v.Status == "not_waiting" {
		return false, nil
	}
	if v.Diagnostic != "" {
		return false, fmt.Errorf("invalid execution demand: %s", v.Diagnostic)
	}
	if v.Status == "unresolved" {
		if !f.IncludeUnresolved {
			return false, nil
		}
		// Unresolved base does not erase already known job overrides.
		if v.Demand == nil {
			return true, nil
		}
	}
	if v.Demand == nil {
		return false, nil
	}
	m, err := Compare(v.Demand.Effective, f.Allocation)
	return len(m) == 0, err
}
