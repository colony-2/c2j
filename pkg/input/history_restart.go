package input

import (
	"encoding/json"
	"strings"

	"github.com/colony-2/jobdb/pkg/jobdb"
)

func historyRestartUnavailable(reason string) HistoryRestartAssociation {
	return HistoryRestartAssociation{UnavailableReason: reason}
}

func historyRestartable(r jobdb.ExecutionRecord) bool {
	var policy struct {
		Restart bool `json:"restart"`
	}
	return r.Version == jobdb.ExecutionPatternVersion && r.Kind == "scope" && r.ID != "" && json.Unmarshal(r.Descriptor, &policy) == nil && policy.Restart
}

// RecordedHistoryRestart is a pure lookup over the selected job's retained
// execution identities. It never replays a recipe or follows another job. Scope
// identity includes enclosing invocation and job/task attempts; chapter ordinals
// are not unique logical identities. Copied prefixes work only when their scope
// records are retained in the selected job. Missing lineage stays unavailable.
func RecordedHistoryRestart(key jobdb.JobKey, run jobdb.GetJobRunResponse, loc HistoryLocator) HistoryRestartAssociation {
	if key != run.Job.JobKey || key.JobId == "" || key.TenantId == "" {
		return historyRestartUnavailable("restart mapping belongs to another job")
	}
	if loc.InvocationID == "" {
		return historyRestartUnavailable("recorded invocation identity unavailable")
	}
	invocation, reason := recordedHistoryInvocation(run, loc)
	if reason != "" {
		return historyRestartUnavailable(reason)
	}
	var target string
	for _, e := range run.Execution {
		r := e.Record
		if !historyRestartable(r) {
			continue
		}
		if r.ID == invocation || strings.HasPrefix(invocation, r.ID+"/attempt:") {
			if len(r.ID) > len(target) {
				target = r.ID
			}
		}
	}
	if target == "" {
		return historyRestartUnavailable("no recorded restartable enclosing scope")
	}
	relationship := HistoryRestartContaining
	if target == invocation {
		relationship = HistoryRestartExact
	}
	return validateHistoryRestart(run, loc, HistoryRestartAssociation{ScopeID: target, Relationship: relationship})
}

// Collection-only locators retain their compatibility synthetic ID. Resolve
// their first captured wait/result using both the logical task ID and ordinal.
func recordedHistoryInvocation(run jobdb.GetJobRunResponse, loc HistoryLocator) (string, string) {
	var ids []string
	for _, e := range run.Execution {
		if e.Record.Version != jobdb.ExecutionPatternVersion {
			continue
		}
		matches := loc.GenerationOrdinal != nil && e.Ordinal == *loc.GenerationOrdinal && e.Record.ID == loc.InvocationID && (e.Record.Kind == "result" || e.Record.Kind == "scope")
		if loc.GenerationOrdinal == nil && len(loc.CollectionOrdinals) > 0 && e.Ordinal == loc.CollectionOrdinals[0] && (e.Record.Kind == "wait" || e.Record.Kind == "result") {
			for _, id := range loc.CollectionInvocationIDs {
				if historyTaskID(e.Record.ID) == id {
					matches = true
				}
			}
		}
		if matches {
			ids = append(ids, e.Record.ID)
		}
	}
	if len(ids) != 1 {
		return "", "recorded occurrence boundary absent or ambiguous"
	}
	return ids[0], ""
}

func validateHistoryRestart(run jobdb.GetJobRunResponse, loc HistoryLocator, a HistoryRestartAssociation) HistoryRestartAssociation {
	if a.ScopeID == "" {
		if a.UnavailableReason == "" {
			a.UnavailableReason = "restart mapping unavailable"
		}
		return historyRestartUnavailable(a.UnavailableReason)
	}
	if a.UnavailableReason != "" {
		return historyRestartUnavailable(a.UnavailableReason)
	}
	switch a.Relationship {
	case HistoryRestartExact, HistoryRestartContaining, HistoryRestartBody:
	default:
		return historyRestartUnavailable("unsupported restart relationship")
	}
	// A supplied mapping cannot redirect a saved occurrence into another
	// invocation or attempt, even when both scopes exist in this snapshot.
	invocation, reason := recordedHistoryInvocation(run, loc)
	if reason != "" {
		return historyRestartUnavailable(reason)
	}
	enclosing := a.ScopeID == invocation || strings.HasPrefix(invocation, a.ScopeID+"/attempt:")
	body := a.Relationship == HistoryRestartBody && strings.HasPrefix(a.ScopeID, invocation+"/attempt:")
	if !enclosing && !body {
		return historyRestartUnavailable("restart scope does not match recorded invocation lineage")
	}
	count := 0
	for _, e := range run.Execution {
		if e.Record.ID == a.ScopeID && historyRestartable(e.Record) {
			count++
		}
	}
	if count != 1 {
		return historyRestartUnavailable("restart scope absent or ambiguous in selected job")
	}
	return a
}
