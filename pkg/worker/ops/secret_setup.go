package ops

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

const SecretSetupTaskType = "secret_setup"

type SecretSetupRequest struct {
	Required   []string `json:"required"`
	Candidates []string `json:"candidates,omitempty"`
}
type SecretSetupResult struct {
	Duration time.Duration `json:"duration_ns"`
	Error    string        `json:"error,omitempty"`
}
type secretSetupWorker struct{ sessions *secrets.Sessions }

func (secretSetupWorker) Name() string { return SecretSetupTaskType }
func (w secretSetupWorker) Run(tc jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	b, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var req SecretSetupRequest
	if err = json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	session := w.sessions.Get(tc.JobKey)
	if session == nil {
		return nil, errors.New("secret setup requires a local job execution")
	}
	ctx, cancel := NewTaskExecutionContext(tc)
	defer cancel()
	start := time.Now()
	err = session.Prepare(ctx, req.Required, req.Candidates, tc.ResolveSecrets)
	result := SecretSetupResult{Duration: time.Since(start)}
	if err != nil {
		result.Error = err.Error()
	}
	// Only diagnostics cross the durable boundary. Plaintext stays in session.
	return jobdb.NewTaskData(result)
}
