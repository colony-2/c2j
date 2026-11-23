// Package jobrecovery replays frozen historical operations through their original
// HTTP receipts. It does not prepare new jobs or provide receipt-only lookup.
package jobrecovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/colony-2/c2j/pkg/jobref"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

// Client is an owner-scoped recovery connection. Supply the same URL and
// authenticated HTTP client as the original owner connection. Never substitute
// it for a lease/delegated submitter. Redirects are not followed.
type Client struct {
	base string
	http *http.Client
}

func New(base string, client *http.Client) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("invalid recovery runtime URL")
	}
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(base, "/"), http: &copy}, nil
}

// ReplaySubmitJobRef accepts only a frozen selected operation with its original
// explicit destination. Selection discovery and server authorization still apply.
func (c *Client) ReplaySubmitJobRef(ctx context.Context, job jobdb.SubmitJob) (jobref.Ref, error) {
	if job.GoalID == "" {
		return jobref.Ref{}, jobdb.ErrPrimaryGoalSelectionUnsupported
	}
	if err := jobdb.ValidatePrimaryGoalSelection(job); err != nil {
		return jobref.Ref{}, err
	}
	if err := (jobdb.JobKey{TenantId: job.TenantId, JobId: job.JobID}).Validate(); err != nil {
		return jobref.Ref{}, err
	}
	var capability struct {
		Supported bool `json:"supported"`
	}
	if err := c.call(ctx, http.MethodGet, "/v1/capabilities/primary-goal-selection", nil, &capability); err != nil {
		return jobref.Ref{}, err
	}
	if !capability.Supported {
		return jobref.Ref{}, jobdb.ErrPrimaryGoalSelectionUnsupported
	}
	body, err := encodeSubmit(ctx, job)
	if err != nil {
		return jobref.Ref{}, err
	}
	var receipt wireHandle
	err = c.call(ctx, http.MethodPut, "/v1/tenants/"+url.PathEscape(job.TenantId)+"/jobs/"+url.PathEscape(job.JobID)+"/primary-goal", map[string]any{"goalId": job.GoalID, "submission": body}, &receipt)
	if err != nil {
		return jobref.Ref{}, err
	}
	ref := receipt.ref()
	return ref, ref.ValidateSelected(job)
}

// ReplayRestartJobRef preserves ShareGoal and all frozen inputs. A receipt miss
// may create the original operation. There is no alternate route or fresh retry.
func (c *Client) ReplayRestartJobRef(ctx context.Context, request jobdb.SubmitRestartJobRequest) (jobref.Ref, error) {
	j := request.Job
	if !j.ShareGoal || strings.TrimSpace(request.RequestID) == "" {
		return jobref.Ref{}, fmt.Errorf("historical recovery requires sharing mode and original request ID")
	}
	if err := j.PriorJobKey.Validate(); err != nil {
		return jobref.Ref{}, err
	}
	body, err := encodeRestart(ctx, request)
	if err != nil {
		return jobref.Ref{}, err
	}
	method, path := http.MethodPost, "/v1/tenants/"+url.PathEscape(j.PriorJobKey.TenantId)+"/jobs/restarts"
	if j.JobID != "" {
		if err := jobdb.ValidateIdentifier(j.JobID); err != nil {
			return jobref.Ref{}, err
		}
		method = http.MethodPut
		path = "/v1/tenants/" + url.PathEscape(j.PriorJobKey.TenantId) + "/jobs/" + url.PathEscape(j.JobID) + "/restart"
	}
	var receipt wireHandle
	if err = c.call(ctx, method, path, body, &receipt); err != nil {
		return jobref.Ref{}, err
	}
	ref := receipt.ref()
	return ref, ref.ValidateSubmission(jobdb.SubmitJob{TenantId: j.PriorJobKey.TenantId, JobID: j.JobID, Cell: j.Cell, CellRepo: j.CellRepo})
}

type wireHandle struct {
	JobKey   jobdb.JobKey `json:"jobKey"`
	GoalID   string       `json:"goalId"`
	Cell     string       `json:"cell"`
	CellID   string       `json:"cellId"`
	CellRepo string       `json:"cellRepo"`
}

func (h wireHandle) ref() jobref.Ref {
	return jobref.Ref{TenantID: h.JobKey.TenantId, JobID: h.JobKey.JobId, GoalID: h.GoalID, Cell: h.Cell, CellID: h.CellID, CellRepo: h.CellRepo}
}

func (c *Client) call(ctx context.Context, method, path string, body any, out any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return jobdb.ClassifyRuntimeIO(err, "historical_receipt", nil, nil)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return fmt.Errorf("historical receipt response too large")
	}
	if resp.StatusCode != http.StatusOK {
		return responseError(resp.StatusCode, raw)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: malformed historical receipt", jobdb.ErrSubmissionReceiptUnavailable)
	}
	return nil
}

func responseError(status int, raw []byte) error {
	var e struct{ Code, Message string }
	_ = json.Unmarshal(raw, &e)
	sentinels := map[string]error{"primary_goal_selection_forbidden": jobdb.ErrPrimaryGoalSelectionForbidden, "primary_goal_selection_unsupported": jobdb.ErrPrimaryGoalSelectionUnsupported, "job_goal_semantics_unsupported": jobdb.ErrJobGoalSemanticsUnsupported, "submission_receipt_unavailable": jobdb.ErrSubmissionReceiptUnavailable, "goal_conflict": jobdb.ErrGoalConflict, "goal_not_found": jobdb.ErrGoalNotFound, "job_not_found": jobdb.ErrJobNotFound, "invalid_dependency": jobdb.ErrInvalidDependency, "dependency_cycle": jobdb.ErrDependencyCycle}
	if cause := sentinels[e.Code]; cause != nil {
		return fmt.Errorf("historical receipt: %w", cause)
	}
	if status == http.StatusForbidden || status == http.StatusUnauthorized {
		return jobdb.ErrLeasePermissionDenied
	}
	if status == http.StatusConflict {
		if e.Code == "existing_job_mismatch" {
			return jobdb.NewExistingJobMismatchError(e.Message)
		}
		return jobdb.ErrConflict
	}
	// Do not log arbitrary server bodies, which may contain request inputs.
	return fmt.Errorf("historical receipt: HTTP %d", status)
}
