package jobutil

import (
	"encoding/json"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

// FormatRoute displays both opaque identifiers without inventing a delimiter
// grammar. It is for diagnostics only; route matching uses the typed value.
func FormatRoute(route jobdb.Route) string {
	raw, _ := json.Marshal(route) // Route consists only of JSON string fields.
	return string(raw)
}
