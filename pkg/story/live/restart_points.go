package live

import (
	"encoding/json"
	"github.com/colony-2/c2j/pkg/story/internal/model"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// RecordedRestartScopes discovers boundaries from durable eligibility evidence.
// It never loads recipes or replays expressions and preserves every recorded visit.
func RecordedRestartScopes(run jobdb.GetJobRunResponse) []model.RestartScope {
	points := make([]model.RestartScope, 0)
	if submission, err := SubmissionRestartScope(run); err == nil {
		points = append(points, submission)
	}
	for _, entry := range run.Execution {
		if !restartableScope(entry) {
			continue
		}
		var labels struct {
			Title string   `json:"title"`
			Path  []string `json:"path"`
		}
		_ = json.Unmarshal(entry.Record.Descriptor, &labels)
		points = append(points, model.RestartScope{ScopeID: entry.Record.ID, ChapterOrdinal: entry.Ordinal, StartedAt: entry.Record.At, Title: labels.Title, Path: labels.Path})
	}
	ids := map[string]int{}
	for _, point := range points {
		ids[point.ScopeID]++
	}
	for i := range points {
		if points[i].ScopeID == "" {
			points[i].UnavailableReason = "Missing scope identity"
		} else if ids[points[i].ScopeID] > 1 {
			points[i].UnavailableReason = "Duplicate scope identity; cannot select uniquely"
		}
	}
	return points
}
