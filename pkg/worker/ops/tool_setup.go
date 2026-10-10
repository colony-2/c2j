package ops

import (
	"context"
	"encoding/json"
	"os"
	"time"

	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/toolenv"
	"github.com/colony-2/jobdb/pkg/jobdb"
	jobworkflow "github.com/colony-2/jobdb/pkg/workflow"
)

const ToolSetupTaskType = "tool_setup"
const ExtensionResolutionTaskType = "extension_resolution"
const SetupTimeout = 30 * time.Minute

type ExtensionResolutionRequest struct {
	Selector string                `json:"selector"`
	Options  extops.ResolveOptions `json:"options"`
}
type ExtensionResolutionResult struct {
	Error  string             `json:"error,omitempty"`
	Op     *extops.ResolvedOp `json:"op"`
	WallMS int64              `json:"wall_ms"`
}
type ToolSetupRequest struct {
	Scopes       []toolenv.Scope    `json:"scopes,omitempty"`
	Extension    *extops.ResolvedOp `json:"extension,omitempty"`
	ResolutionMS int64              `json:"resolution_ms,omitempty"`
}
type ToolSetupResult struct {
	Pending     bool                 `json:"pending,omitempty"`
	Duration    time.Duration        `json:"duration_ns"`
	Environment *toolenv.Environment `json:"environment,omitempty"`
	Extension   *extops.ResolvedOp   `json:"extension,omitempty"`
	Diagnostics toolenv.Diagnostics  `json:"diagnostics"`
}

func (r *ToolSetupResult) Ready() bool {
	if r == nil {
		return true
	}
	if r.Pending {
		return false
	}
	if r.Environment != nil && !r.Environment.Ready() {
		return false
	}
	if r.Extension != nil && !r.Extension.Ready() {
		return false
	}
	return true
}

type toolSetupWorker struct{}

func NewToolSetupWorker() jobworkflow.TaskWorker { return toolSetupWorker{} }
func (toolSetupWorker) Name() string             { return ToolSetupTaskType }
func (toolSetupWorker) Run(tc jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	var req ToolSetupRequest
	b, err := input.GetData()
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	ctx, cancel := NewTaskExecutionContext(tc)
	defer cancel()
	result := PrepareTools(ctx, req)
	result.Diagnostics.TaskOrdinal = &tc.Step
	// Setup failures are durable diagnostics, distinct from task execution errors.
	return jobdb.NewTaskData(result)
}

// PrepareTools is shared by task workers and native recipe-test passthrough.
// Callers supply the setup context, separate from the op execution timeout.
func PrepareTools(ctx context.Context, req ToolSetupRequest) ToolSetupResult {
	start := time.Now()
	result := ToolSetupResult{Extension: req.Extension}
	// If source storage was lost after manifest resolution, restore only the
	// already-pinned selector. The durable manifest remains authoritative.
	if req.Extension != nil && req.Extension.Nix != nil {
		prepared, reused, e := extops.PrepareNixOp(ctx, req.Extension)
		outcome := "prepared"
		if reused {
			outcome = "reused"
		}
		if e != nil {
			outcome = "failed"
			result.Diagnostics.Error = e.Error()
		} else {
			result.Extension = prepared
		}
		result.Diagnostics.Tools = append(result.Diagnostics.Tools, toolenv.ToolDiagnostic{
			Reference: req.Extension.Selector, Identity: req.Extension.Nix.StorePath,
			Scope: "op", WallMS: time.Since(start).Milliseconds(), Outcome: outcome,
		})
	} else if req.Extension != nil {
		if _, e := os.Stat(req.Extension.SpecPath); e != nil {
			selector := req.Extension.ResolvedSelector
			if selector == "" {
				selector = req.Extension.Selector
			}
			path, e := extops.ResolvePath(ctx, selector, extops.ResolveOptions{BaseDir: req.Extension.ProjectRoot})
			if e != nil {
				result.Diagnostics.Error = e.Error()
			} else {
				clone := *req.Extension
				clone.ProjectRoot = path.ProjectRoot
				clone.OpDir = path.Dir
				clone.SpecPath = path.Dir + "/op.yaml"
				if _, e := os.Stat(clone.SpecPath); e != nil {
					clone.SpecPath = path.Dir + "/op.yml"
				}
				result.Extension = &clone
			}
		}
	}
	if len(req.Scopes) > 0 && result.Diagnostics.Error == "" {
		manager, e := toolenv.Default()
		if e != nil {
			result.Diagnostics.Error = e.Error()
		} else {
			env, diag, _ := manager.Prepare(ctx, req.Scopes)
			result.Environment = &env
			result.Diagnostics.Tools = append(result.Diagnostics.Tools, diag.Tools...)
			result.Diagnostics.Error = diag.Error
		}
	}
	result.Duration = time.Since(start)
	result.Diagnostics.WallMS = result.Duration.Milliseconds() + req.ResolutionMS
	return result
}

type extensionResolutionWorker struct{}

func NewExtensionResolutionWorker() jobworkflow.TaskWorker { return extensionResolutionWorker{} }
func (extensionResolutionWorker) Name() string             { return ExtensionResolutionTaskType }
func (extensionResolutionWorker) Run(tc jobworkflow.TaskContext, input jobdb.TaskData) (jobdb.TaskData, error) {
	b, err := input.GetData()
	if err != nil {
		return nil, err
	}
	var req ExtensionResolutionRequest
	if err = json.Unmarshal(b, &req); err != nil {
		return nil, err
	}
	ctx, cancel := NewTaskExecutionContext(tc)
	defer cancel()
	start := time.Now()
	op, err := extops.Resolve(ctx, req.Selector, req.Options)
	result := ExtensionResolutionResult{Op: op, WallMS: time.Since(start).Milliseconds()}
	if err != nil {
		result.Error = err.Error()
	}
	return jobdb.NewTaskData(result)
}
