// Package core defines the core domain types and interfaces used across all colony2 modules.
package core

// Cell represents a single cell in the dependency graph.
type Cell struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Type         string   `json:"type"`
	Dependencies []string `json:"dependencies"` // List of cell IDs this cell depends on
}

type CellName string

// Edge represents a directed edge in the dependency graph.
type Edge struct {
	ID     string `json:"id"`
	Source string `json:"source"` // Cell ID of the source
	Target string `json:"target"` // Cell ID of the target
}

// Graph represents the complete dependency graph.
type Graph struct {
	Cells []Cell `json:"cells"`
	Edges []Edge `json:"edges"`
}

// Position represents the visual position of a cell in the UI.
type Position struct {
	CellID string  `json:"cellId"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
}
