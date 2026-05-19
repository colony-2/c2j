package recipe

import recipeartifacts "github.com/colony-2/c2j/pkg/artifacts"

type FailureKind string

const (
	FailureKindTimeout      FailureKind = "timeout"
	FailureKindTaskError    FailureKind = "task_error"
	FailureKindSystemError  FailureKind = "system_error"
	FailureKindCancellation FailureKind = "cancellation"
	FailureKindUnknown      FailureKind = "unknown"
)

type FailureNodeType string

const (
	FailureNodeOp           FailureNodeType = "op"
	FailureNodeSequence     FailureNodeType = "sequence"
	FailureNodeStateMachine FailureNodeType = "state_machine"
	FailureNodeState        FailureNodeType = "state"
)

type RuntimeFailure struct {
	Kind      FailureKind                    `json:"kind"`
	Code      string                         `json:"code,omitempty"`
	Message   string                         `json:"message"`
	Retryable bool                           `json:"retryable"`
	Node      FailureNode                    `json:"node"`
	Timing    *FailureTiming                 `json:"timing,omitempty"`
	Task      *FailureTask                   `json:"task,omitempty"`
	Artifacts map[string]recipeartifacts.Ref `json:"artifacts,omitempty"`
	Attrs     map[string]interface{}         `json:"attrs,omitempty"`
	Cause     *RuntimeFailure                `json:"cause,omitempty"`
}

type FailureNode struct {
	ID   string          `json:"id,omitempty"`
	Path string          `json:"path,omitempty"`
	Type FailureNodeType `json:"type"`
	Op   string          `json:"op,omitempty"`
}

type FailureTiming struct {
	Timeout string `json:"timeout,omitempty"`
	Scope   string `json:"scope,omitempty"`
	After   string `json:"after,omitempty"`
}

type FailureTask struct {
	Status     string `json:"status,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	StderrTail string `json:"stderr_tail,omitempty"`
	StdoutTail string `json:"stdout_tail,omitempty"`
}

func (f *RuntimeFailure) Clone() *RuntimeFailure {
	if f == nil {
		return nil
	}
	out := *f
	if f.Timing != nil {
		timing := *f.Timing
		out.Timing = &timing
	}
	if f.Task != nil {
		task := *f.Task
		if f.Task.ExitCode != nil {
			exitCode := *f.Task.ExitCode
			task.ExitCode = &exitCode
		}
		out.Task = &task
	}
	if f.Artifacts != nil {
		out.Artifacts = make(map[string]recipeartifacts.Ref, len(f.Artifacts))
		for key, value := range f.Artifacts {
			out.Artifacts[key] = value
		}
	}
	if f.Attrs != nil {
		out.Attrs = cloneInterfaceMap(f.Attrs)
	}
	out.Cause = f.Cause.Clone()
	return &out
}

func cloneInterfaceMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = cloneInterfaceValue(value)
	}
	return out
}

func cloneInterfaceValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneInterfaceMap(typed)
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = cloneInterfaceValue(item)
		}
		return out
	default:
		return typed
	}
}
