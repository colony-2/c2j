package recipetest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inputop "github.com/colony-2/c2j/pkg/input"
	"github.com/colony-2/c2j/pkg/worker/commandop"
	"github.com/stretchr/testify/require"
)

func TestResponsePersistenceLeaseExpiryIsExact(t *testing.T) {
	c, _ := responsePersistenceCase(false, "response_output")
	f := &runtimeFixture{c: c, root: t.TempDir(), controlledResponses: map[int]*controlledResponse{}, scenario: &scenarioState{stopped: true, report: &ScenarioReport{Sessions: []ScenarioSession{{ID: 1, Kind: "joined"}}}}}
	a := &SubmissionAttempt{ID: "faulted", Attempted: true, FaultStage: "response_output", Fault: &ResponseFaultEvidence{Removed: true, Hits: 1}, Pending: PendingResponseIdentity{TenantID: "t", JobID: "j"}, After: &ResponseStateSnapshot{Lease: &ResponseRecoveryLease{ID: "captured", WorkerID: "owner", ExpiresAtNS: 100}}}
	f.controlledResponses[0] = &controlledResponse{evidence: a}
	db, err := responseDB(f.root)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE jobdb_jobs(tenant_id TEXT,job_id TEXT,lease_id TEXT,lease_worker_id TEXT,lease_expires_at_ns INTEGER,route_task_type TEXT)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO jobdb_jobs VALUES('t','j','changed','owner',100,''),('t','unrelated','captured','owner',100,'')`)
	require.NoError(t, err)
	require.ErrorContains(t, f.expireResponseLease(context.Background(), "faulted"), "changed")
	_, err = db.Exec(`UPDATE jobdb_jobs SET lease_id='captured' WHERE job_id='j'`)
	require.NoError(t, err)
	f.scenario.stopped = false
	require.ErrorContains(t, f.expireResponseLease(context.Background(), "faulted"), "joined")
	f.scenario.stopped = true
	a.Fault.Removed = false
	require.Error(t, f.expireResponseLease(context.Background(), "faulted"))
	a.Fault.Removed = true
	require.NoError(t, f.expireResponseLease(context.Background(), "faulted"))
	require.ErrorContains(t, f.expireResponseLease(context.Background(), "faulted"), "already")
	var expiry int64
	require.NoError(t, db.QueryRow(`SELECT lease_expires_at_ns FROM jobdb_jobs WHERE job_id='unrelated'`).Scan(&expiry))
	require.EqualValues(t, 100, expiry)
	require.NoError(t, db.QueryRow(`SELECT lease_expires_at_ns FROM jobdb_jobs WHERE job_id='j'`).Scan(&expiry))
	require.Less(t, expiry, time.Now().UnixNano())
	require.FileExists(t, filepath.Join(f.root, "jobs.db"))
}
func TestResponseSubmissionRequiresJoinedWorkers(t *testing.T) {
	withRegisteredCoreOps(t, inputop.GetOp(), commandop.GetOp())
	c, target := responsePersistenceCase(false, "healthy")
	c.Runtime.Responses[0].Submission.AfterAll = []string{"pending"}
	c.Runtime.Controls.Scenario.Actions[0].AfterAll = []string{"attempted"}
	r := runResponsePersistence(t, c, target)
	require.Equal(t, "failed", r.Status)
	require.Contains(t, strings.Join(r.Runtime.Controls.Errors, " "), "stopped and joined")
	require.False(t, r.Runtime.Submissions[0].Attempted)
}

func TestResponsePersistenceTriggerIsolationAndCleanup(t *testing.T) {
	require.NoError(t, registerResponseFaultProbe())
	for _, stage := range []string{"claim", "response_output", "settlement"} {
		t.Run(stage, func(t *testing.T) {
			c, _ := responsePersistenceCase(false, "healthy")
			f := &runtimeFixture{c: c, root: t.TempDir(), scenario: &scenarioState{stopped: true, report: &ScenarioReport{Sessions: []ScenarioSession{{ID: 1, Kind: "joined"}}}}}
			db, err := responseDB(f.root)
			require.NoError(t, err)
			defer db.Close()
			_, err = db.Exec(`CREATE TABLE jobdb_jobs(tenant_id TEXT,job_id TEXT,payload BLOB,route_task_type TEXT,lease_id TEXT); CREATE TABLE jobdb_chapter_chapters(anthology_id TEXT,story_id TEXT,ordinal INTEGER)`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO jobdb_jobs VALUES('t','selected',x'010203','input:collect_user_input',NULL),('t','other',x'010203','input:collect_user_input',NULL)`)
			require.NoError(t, err)
			a := &SubmissionAttempt{FaultStage: stage, Pending: PendingResponseIdentity{TenantID: "t", JobID: "selected", OutputOrdinal: 42, SchedulerSHA256: contentDigest([]byte{1, 2, 3})}}
			fault, err := f.armResponseFault(context.Background(), a)
			require.NoError(t, err)
			defer func() {
				if !fault.evidence.Removed {
					require.NoError(t, fault.remove(context.Background()))
				}
			}()
			// Writes to another job, another chapter and another tenant stay healthy
			// while the selected trigger is installed.
			_, err = db.Exec(`UPDATE jobdb_jobs SET route_task_type='',lease_id='other-lease' WHERE job_id='other'`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO jobdb_chapter_chapters VALUES('t','other',42),('t','selected',43),('other-tenant','selected',42)`)
			require.NoError(t, err)
			switch stage {
			case "claim":
				_, err = db.Exec(`UPDATE jobdb_jobs SET route_task_type='',lease_id='selected-lease' WHERE job_id='selected'`)
			case "response_output":
				_, err = db.Exec(`INSERT INTO jobdb_chapter_chapters VALUES('t','selected',42)`)
			case "settlement":
				_, err = db.Exec(`UPDATE jobdb_jobs SET route_task_type='',lease_id='selected-lease',payload=x'04' WHERE job_id='selected'`)
				require.NoError(t, err)
				_, err = db.Exec(`INSERT INTO jobdb_chapter_chapters VALUES('t','selected',42)`)
				require.NoError(t, err)
				_, err = db.Exec(`UPDATE jobdb_jobs SET lease_id=NULL WHERE job_id='other'`)
				require.NoError(t, err)
				_, err = db.Exec(`UPDATE jobdb_jobs SET lease_id=NULL WHERE job_id='selected'`)
			}
			require.ErrorContains(t, err, "c2j response persistence fault")
			require.EqualValues(t, 1, fault.hits.Load())
			require.NoError(t, fault.remove(context.Background()))
			require.True(t, a.Fault.Removed)
			var n int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger'`).Scan(&n))
			require.Zero(t, n)
			// The same selected write is healthy after removal.
			if stage == "response_output" {
				_, err = db.Exec(`INSERT INTO jobdb_chapter_chapters VALUES('t','selected',42)`)
			} else {
				_, err = db.Exec(`UPDATE jobdb_jobs SET route_task_type='',lease_id=NULL WHERE job_id='selected'`)
			}
			require.NoError(t, err)
		})
	}
}
