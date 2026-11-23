package recipetest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"modernc.org/sqlite"
)

// The nontransactional driver counter proves a rejected write reached its
// trigger even when JobDB translates the SQLite error. It never changes storage
// or the API result. Tokens exist only while a case-local trigger is armed.
var responseFaultProbe struct {
	once sync.Once
	err  error
	hits sync.Map // token -> *responseFault
}

func registerResponseFaultProbe() error {
	responseFaultProbe.once.Do(func() {
		responseFaultProbe.err = sqlite.RegisterScalarFunction("c2j_recipe_test_fault_hit", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			token, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("invalid response fault token")
			}
			hit, ok := responseFaultProbe.hits.Load(token)
			if !ok {
				return nil, fmt.Errorf("inactive response fault token")
			}
			hit.(*responseFault).hits.Add(1)
			return int64(1), nil
		})
		if responseFaultProbe.err != nil {
			return
		}
		responseFaultProbe.err = sqlite.RegisterScalarFunction("c2j_recipe_test_response_lease", 3, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			rf, ok := responseFaultProbe.hits.Load(args[0])
			if !ok {
				return nil, fmt.Errorf("inactive response fault token")
			}
			lease, ok := args[1].(string)
			if !ok {
				return int64(0), nil
			}
			f := rf.(*responseFault)
			if args[2] == "capture" {
				if f.lease != "" {
					return nil, fmt.Errorf("duplicate response claim")
				}
				f.lease = lease
				return int64(1), nil
			}
			if f.lease != "" && f.lease == lease {
				return int64(1), nil
			}
			return int64(0), nil
		})
	})
	return responseFaultProbe.err
}

type ResponseFaultEvidence struct {
	Stage        string `json:"stage"`
	Trigger      string `json:"trigger"`
	Predicate    string `json:"predicate"`
	Armed        bool   `json:"armed"`
	Hits         int64  `json:"hits"`
	Removed      bool   `json:"removed"`
	ClaimTrigger string `json:"claim_trigger,omitempty"`
	ClaimedLease string `json:"claimed_lease,omitempty"`
}
type responseFault struct {
	db       *sql.DB
	evidence *ResponseFaultEvidence
	token    string
	hits     *atomic.Int64
	lease    string
}

func responseDB(root string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", filepath.Join(root, "jobs.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
func sqlLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func (f *runtimeFixture) armResponseFault(ctx context.Context, a *SubmissionAttempt) (*responseFault, error) {
	if !f.workersJoined() {
		return nil, fmt.Errorf("response fault requires stopped and joined workers")
	}
	db, err := responseDB(f.root)
	if err != nil {
		return nil, err
	}
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	rf := &responseFault{db: db, token: token, hits: &atomic.Int64{}, evidence: &ResponseFaultEvidence{Stage: a.FaultStage, Trigger: "c2j_response_" + token}}
	p := a.Pending
	job := "OLD.tenant_id=" + sqlLiteral(p.TenantID) + " AND OLD.job_id=" + sqlLiteral(p.JobID)
	// The pinned writer drops task coordinates at claim and clears the lease at
	// settlement. Match the exact captured scheduler blob without depending on
	// JobDB's private protobuf decoder.
	var payload []byte
	if err = db.QueryRowContext(ctx, "SELECT payload FROM jobdb_jobs WHERE tenant_id=? AND job_id=?", p.TenantID, p.JobID).Scan(&payload); err != nil {
		db.Close()
		return nil, err
	}
	if contentDigest(payload) != p.SchedulerSHA256 {
		db.Close()
		return nil, fmt.Errorf("pending scheduler payload changed")
	}
	wait := " AND OLD.payload=X'" + hex.EncodeToString(payload) + "'"
	table, operation := "jobdb_jobs", "UPDATE"
	switch a.FaultStage {
	case "claim":
		rf.evidence.Predicate = job + wait + " AND OLD.route_task_type='input:collect_user_input' AND NEW.route_task_type='' AND NEW.lease_id IS NOT NULL"
	case "response_output":
		table, operation = "jobdb_chapter_chapters", "INSERT"
		rf.evidence.Predicate = fmt.Sprintf("NEW.anthology_id=%s AND NEW.story_id=%s AND NEW.ordinal=%d", sqlLiteral(p.TenantID), sqlLiteral(p.JobID), p.OutputOrdinal)
	case "settlement":
		// Capture the lease from the exact selected claim, then reject only its
		// settlement after the selected output ordinal exists.
		rf.evidence.ClaimTrigger = rf.evidence.Trigger + "_claim"
		rf.evidence.Predicate = job + " AND c2j_recipe_test_response_lease(" + sqlLiteral(token) + ",OLD.lease_id,'matches')=1 AND OLD.route_task_type='' AND OLD.lease_id IS NOT NULL AND NEW.lease_id IS NULL AND EXISTS (SELECT 1 FROM jobdb_chapter_chapters WHERE anthology_id=" + sqlLiteral(p.TenantID) + " AND story_id=" + sqlLiteral(p.JobID) + fmt.Sprintf(" AND ordinal=%d)", p.OutputOrdinal)
	default:
		db.Close()
		return nil, fmt.Errorf("unsupported response fault %s", a.FaultStage)
	}
	responseFaultProbe.hits.Store(token, rf)
	if a.FaultStage == "settlement" {
		claimSQL := "CREATE TRIGGER " + rf.evidence.ClaimTrigger + " AFTER UPDATE ON jobdb_jobs WHEN " + job + wait + " AND OLD.route_task_type='input:collect_user_input' AND NEW.route_task_type='' AND NEW.lease_id IS NOT NULL BEGIN SELECT c2j_recipe_test_response_lease(" + sqlLiteral(token) + ",NEW.lease_id,'capture'); END"
		if _, err = db.ExecContext(ctx, claimSQL); err != nil {
			responseFaultProbe.hits.Delete(token)
			db.Close()
			return nil, err
		}
	}
	statement := "CREATE TRIGGER " + rf.evidence.Trigger + " BEFORE " + operation + " ON " + table + " WHEN " + rf.evidence.Predicate + " BEGIN SELECT c2j_recipe_test_fault_hit(" + sqlLiteral(token) + "); SELECT RAISE(ABORT, 'c2j response persistence fault'); END"
	if _, err = db.ExecContext(ctx, statement); err != nil {
		_ = rf.remove(context.WithoutCancel(ctx))
		return nil, err
	}
	rf.evidence.Armed = true
	a.Fault = rf.evidence
	return rf, nil
}
func (rf *responseFault) remove(ctx context.Context) error {
	defer rf.db.Close()
	defer responseFaultProbe.hits.Delete(rf.token)
	rf.evidence.Hits = rf.hits.Load()
	rf.evidence.ClaimedLease = rf.lease
	if rf.evidence.ClaimTrigger != "" {
		if _, err := rf.db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+rf.evidence.ClaimTrigger); err != nil {
			return err
		}
	}
	if _, err := rf.db.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+rf.evidence.Trigger); err != nil {
		return err
	}
	var n int
	if err := rf.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN (?,?)", rf.evidence.Trigger, rf.evidence.ClaimTrigger).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("response trigger still installed")
	}
	rf.evidence.Removed = true
	return nil
}
func (f *runtimeFixture) workersJoined() bool {
	if f.scenario == nil || !f.scenario.stopped {
		return false
	}
	for i := len(f.scenario.report.Sessions) - 1; i >= 0; i-- {
		e := f.scenario.report.Sessions[i]
		if e.Kind == "joined" {
			return true
		}
		if e.Kind == "started" {
			return false
		}
	}
	return false
}
func (f *runtimeFixture) expireResponseLease(ctx context.Context, id string) error {
	r := f.controlledResponseByID(id)
	if r == nil || !r.evidence.Attempted || r.evidence.After == nil {
		return fmt.Errorf("response %s has no recorded recovery lease", id)
	}
	a := r.evidence
	if !f.workersJoined() || a.Fault == nil || !a.Fault.Removed || a.Fault.Hits != 1 || (a.FaultStage != "response_output" && a.FaultStage != "settlement") {
		return fmt.Errorf("lease expiry requires joined workers and removed reached recovery fault")
	}
	lease := a.After.Lease
	if lease == nil || lease.ID == "" || r.leaseExpired {
		return fmt.Errorf("absent or already expired response lease")
	}
	db, err := responseDB(f.root)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.ExecContext(ctx, `UPDATE jobdb_jobs SET lease_expires_at_ns=? WHERE tenant_id=? AND job_id=? AND lease_id=? AND lease_worker_id=? AND lease_expires_at_ns=? AND route_task_type=''`, time.Now().Add(-time.Second).UnixNano(), a.Pending.TenantID, a.Pending.JobID, lease.ID, lease.WorkerID, lease.ExpiresAtNS)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("recorded response recovery lease changed")
	}
	r.leaseExpired = true
	return nil
}
