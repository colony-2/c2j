package jobcontext

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	CurrentContextVersionEnv     = "C2J_CURRENT_CONTEXT_VERSION"
	CurrentTenantIDEnv           = "C2J_CURRENT_TENANT_ID"
	CurrentJobIDEnv              = "C2J_CURRENT_JOB_ID"
	CurrentJobTypeEnv            = "C2J_CURRENT_JOB_TYPE"
	CurrentOpTypeEnv             = "C2J_CURRENT_OP_TYPE"
	CurrentOpStepEnv             = "C2J_CURRENT_OP_STEP"
	CurrentOpTaskTypeEnv         = "C2J_CURRENT_OP_TASK_TYPE"
	CurrentCellNameEnv           = "C2J_CURRENT_CELL_NAME"
	CurrentRepositorySourceEnv   = "C2J_CURRENT_REPOSITORY_SOURCE"
	CurrentGitRefEnv             = "C2J_CURRENT_GIT_REF"
	CurrentInvocationPathEnv     = "C2J_CURRENT_INVOCATION_PATH"
	CurrentInvocationSequenceEnv = "C2J_CURRENT_INVOCATION_SEQUENCE"
	CurrentInvocationHashEnv     = "C2J_CURRENT_INVOCATION_HASH"

	// TenantIDEnv is kept as a convenience for child tools that still consume a
	// tenant-only environment default. The current c2j CLI resolves through
	// C2J_JOBDB instead.
	TenantIDEnv = "C2J_TENANT_ID"

	CurrentContextVersion = "1"
)

type Current struct {
	TenantID           string `json:"tenant_id,omitempty"`
	JobID              string `json:"job_id,omitempty"`
	JobType            string `json:"job_type,omitempty"`
	OpType             string `json:"op_type,omitempty"`
	OpStep             string `json:"op_step,omitempty"`
	OpTaskType         string `json:"op_task_type,omitempty"`
	CellName           string `json:"cell_name,omitempty"`
	RepositorySource   string `json:"repo,omitempty"`
	GitRef             string `json:"git_ref,omitempty"`
	InvocationPath     string `json:"invocation_path,omitempty"`
	InvocationSequence int64  `json:"invocation_seq,omitempty"`
	InvocationHash     string `json:"invocation_hash,omitempty"`
}

type Parent struct {
	TenantID           string `json:"tenant_id,omitempty"`
	JobID              string `json:"job_id,omitempty"`
	JobType            string `json:"job_type,omitempty"`
	OpType             string `json:"op_type,omitempty"`
	OpStep             string `json:"op_step,omitempty"`
	OpTaskType         string `json:"op_task_type,omitempty"`
	CellName           string `json:"cell_name,omitempty"`
	RepositorySource   string `json:"repo,omitempty"`
	GitRef             string `json:"git_ref,omitempty"`
	InvocationPath     string `json:"invocation_path,omitempty"`
	InvocationSequence int64  `json:"invocation_seq,omitempty"`
	InvocationHash     string `json:"invocation_hash,omitempty"`
}

type StartedJobContext struct {
	TenantID             string `json:"tenant_id,omitempty"`
	JobID                string `json:"job_id,omitempty"`
	RecipeName           string `json:"recipe,omitempty"`
	Status               string `json:"status,omitempty"`
	ParentInvocationHash string `json:"parent_invocation_hash,omitempty"`
}

type StartedJobsContext struct {
	JobIDs []string            `json:"job_ids,omitempty"`
	Items  []StartedJobContext `json:"items,omitempty"`
}

func EmptyStartedJobsContext() StartedJobsContext {
	return StartedJobsContext{
		JobIDs: []string{},
		Items:  []StartedJobContext{},
	}
}

func (c Current) HasJob() bool {
	return strings.TrimSpace(c.TenantID) != "" && strings.TrimSpace(c.JobID) != ""
}

func (p Parent) HasJob() bool {
	return strings.TrimSpace(p.TenantID) != "" && strings.TrimSpace(p.JobID) != ""
}

func ParentFromCurrent(c Current) Parent {
	return Parent{
		TenantID:           strings.TrimSpace(c.TenantID),
		JobID:              strings.TrimSpace(c.JobID),
		JobType:            strings.TrimSpace(c.JobType),
		OpType:             strings.TrimSpace(c.OpType),
		OpStep:             strings.TrimSpace(c.OpStep),
		OpTaskType:         strings.TrimSpace(c.OpTaskType),
		CellName:           strings.TrimSpace(c.CellName),
		RepositorySource:   strings.TrimSpace(c.RepositorySource),
		GitRef:             strings.TrimSpace(c.GitRef),
		InvocationPath:     strings.TrimSpace(c.InvocationPath),
		InvocationSequence: c.InvocationSequence,
		InvocationHash:     strings.TrimSpace(c.InvocationHash),
	}
}

func EnvForCurrent(c Current) map[string]string {
	env := map[string]string{}
	if !c.HasJob() {
		return env
	}
	env[CurrentContextVersionEnv] = CurrentContextVersion
	env[CurrentTenantIDEnv] = strings.TrimSpace(c.TenantID)
	env[CurrentJobIDEnv] = strings.TrimSpace(c.JobID)
	env[TenantIDEnv] = strings.TrimSpace(c.TenantID)
	setIfNotEmpty(env, CurrentJobTypeEnv, c.JobType)
	setIfNotEmpty(env, CurrentOpTypeEnv, c.OpType)
	setIfNotEmpty(env, CurrentOpStepEnv, c.OpStep)
	setIfNotEmpty(env, CurrentOpTaskTypeEnv, c.OpTaskType)
	setIfNotEmpty(env, CurrentCellNameEnv, c.CellName)
	setIfNotEmpty(env, CurrentRepositorySourceEnv, c.RepositorySource)
	setIfNotEmpty(env, CurrentGitRefEnv, c.GitRef)
	setIfNotEmpty(env, CurrentInvocationPathEnv, c.InvocationPath)
	env[CurrentInvocationSequenceEnv] = strconv.FormatInt(c.InvocationSequence, 10)
	setIfNotEmpty(env, CurrentInvocationHashEnv, c.InvocationHash)
	return env
}

func MergeProtectedEnv(base map[string]string, protected map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range protected {
		out[k] = v
	}
	return out
}

func CurrentFromEnv(getenv func(string) string) (Current, bool, error) {
	if getenv == nil {
		return Current{}, false, nil
	}
	current := Current{
		TenantID:         strings.TrimSpace(getenv(CurrentTenantIDEnv)),
		JobID:            strings.TrimSpace(getenv(CurrentJobIDEnv)),
		JobType:          strings.TrimSpace(getenv(CurrentJobTypeEnv)),
		OpType:           strings.TrimSpace(getenv(CurrentOpTypeEnv)),
		OpStep:           strings.TrimSpace(getenv(CurrentOpStepEnv)),
		OpTaskType:       strings.TrimSpace(getenv(CurrentOpTaskTypeEnv)),
		CellName:         strings.TrimSpace(getenv(CurrentCellNameEnv)),
		RepositorySource: strings.TrimSpace(getenv(CurrentRepositorySourceEnv)),
		GitRef:           strings.TrimSpace(getenv(CurrentGitRefEnv)),
		InvocationPath:   strings.TrimSpace(getenv(CurrentInvocationPathEnv)),
		InvocationHash:   strings.TrimSpace(getenv(CurrentInvocationHashEnv)),
	}

	seqRaw := strings.TrimSpace(getenv(CurrentInvocationSequenceEnv))
	if seqRaw != "" {
		seq, err := strconv.ParseInt(seqRaw, 10, 64)
		if err != nil {
			return Current{}, true, fmt.Errorf("%s must be an integer: %w", CurrentInvocationSequenceEnv, err)
		}
		current.InvocationSequence = seq
	}

	if !hasAnyCurrentEnv(current, seqRaw) {
		return Current{}, false, nil
	}
	if current.TenantID == "" || current.JobID == "" {
		return Current{}, true, fmt.Errorf("%s and %s are required when c2j current job context is present", CurrentTenantIDEnv, CurrentJobIDEnv)
	}
	return current, true, nil
}

func ParentFromEnv(getenv func(string) string) (Parent, bool, error) {
	current, ok, err := CurrentFromEnv(getenv)
	if err != nil || !ok {
		return Parent{}, ok, err
	}
	return ParentFromCurrent(current), true, nil
}

func setIfNotEmpty(env map[string]string, key string, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		env[key] = value
	}
}

func hasAnyCurrentEnv(c Current, seqRaw string) bool {
	return c.TenantID != "" ||
		c.JobID != "" ||
		c.JobType != "" ||
		c.OpType != "" ||
		c.OpStep != "" ||
		c.OpTaskType != "" ||
		c.CellName != "" ||
		c.RepositorySource != "" ||
		c.GitRef != "" ||
		c.InvocationPath != "" ||
		c.InvocationHash != "" ||
		strings.TrimSpace(seqRaw) != ""
}
