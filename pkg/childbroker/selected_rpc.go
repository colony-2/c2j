package childbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/colony-2/c2j/pkg/jobcontext"
	"github.com/colony-2/c2j/pkg/starter"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func prepareSelectedRPC(ctx context.Context, broker jobcontext.ChildJobBroker, req SubmitRequest) (starter.Descriptor, error) {
	var out starter.Descriptor
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, broker.Endpoint, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+broker.Token)
	r.Header.Set(sessionHeader, broker.SessionID)
	client := &http.Client{Timeout: defaultClientTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(r)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return out, decodeSelectionError(resp.StatusCode, string(body), true)
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	if err != nil {
		return out, err
	}
	job, err := out.Request()
	if err == nil && (job.GoalID != req.Start.GoalID || job.JobID != req.Start.JobID || job.TenantId != req.Start.TenantId) {
		err = jobdb.ErrSubmissionReceiptUnavailable
	}
	return out, err
}
