package execution

import "github.com/colony-2/jobdb/pkg/jobdb"

// Handoff records why an execution needs a different allocation.
type Handoff struct {
	Kind       string       `json:"kind"`
	JobKey     jobdb.JobKey `json:"job"`
	Demand     Demand       `json:"demand"`
	Allocation Allocation   `json:"allocation"`
	Mismatches []Mismatch   `json:"mismatches,omitempty"`
	Published  bool         `json:"published"`
}
