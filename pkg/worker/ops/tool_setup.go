package ops

import (
	"context"
	"encoding/json"
	"os"
	"time"

	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/toolenv"
)

type ExtensionResolutionRequest struct {
	TemplateContext json.RawMessage       `json:"template_context,omitempty"`
	RawInputs       map[string]any        `json:"raw_inputs,omitempty"`
	Selector        string                `json:"selector"`
	Options         extops.ResolveOptions `json:"options"`
}
type ToolSetupRequest struct {
	Scopes       []toolenv.Scope    `json:"scopes,omitempty"`
	Extension    *extops.ResolvedOp `json:"extension,omitempty"`
	ResolutionMS int64              `json:"resolution_ms,omitempty"`
}
type ToolSetupResult struct {
	Duration    time.Duration        `json:"duration_ns"`
	Environment *toolenv.Environment `json:"environment,omitempty"`
	Extension   *extops.ResolvedOp   `json:"extension,omitempty"`
	Diagnostics toolenv.Diagnostics  `json:"diagnostics"`
}

// PrepareTools is shared by task workers and native recipe-test passthrough.
// Callers supply the consuming task context, including its execution timeout.
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
