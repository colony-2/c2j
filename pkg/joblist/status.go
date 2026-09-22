package joblist

import "github.com/colony-2/jobdb/pkg/jobdb"

// DefaultVisibleStatuses returns a fresh slice of the CLI's nonterminal statuses.
func DefaultVisibleStatuses() []jobdb.JobStatus {
	return []jobdb.JobStatus{
		jobdb.JobStatusReady,
		jobdb.JobStatusExpired,
		jobdb.JobStatusPendingJobs,
		jobdb.JobStatusAwaitingFuture,
		jobdb.JobStatusActive,
		jobdb.JobStatusCrashConcern,
	}
}

// StoresForStatuses selects active and/or archived storage as the CLI does.
func StoresForStatuses(statuses []jobdb.JobStatus) []jobdb.JobStore {
	if len(statuses) == 0 {
		return nil
	}
	hasActive := false
	hasArchived := false
	for _, status := range statuses {
		switch status {
		case jobdb.JobStatusCancelled, jobdb.JobStatusCompleted:
			hasArchived = true
		default:
			hasActive = true
		}
	}

	switch {
	case hasActive && hasArchived:
		return []jobdb.JobStore{jobdb.JobStoreActive, jobdb.JobStoreArchived}
	case hasArchived:
		return []jobdb.JobStore{jobdb.JobStoreArchived}
	default:
		return []jobdb.JobStore{jobdb.JobStoreActive}
	}
}

// StoreForJob reports the store indicated by the job's archive timestamp.
func StoreForJob(job jobdb.JobSummary) jobdb.JobStore {
	if job.ArchivedAt != nil {
		return jobdb.JobStoreArchived
	}
	return jobdb.JobStoreActive
}
