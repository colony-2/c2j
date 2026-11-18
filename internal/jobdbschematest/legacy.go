// Package jobdbschematest retains immutable historical schemas for replay checks.
package jobdbschematest

import _ "embed"

// LegacyGoalSchema predates metadata titles and grouped contributions. Keep this
// fixture independent of the current schema so current edits cannot rewrite history.
//
//go:embed legacy-goal-schema.json
var LegacyGoalSchema []byte
