package compiler

import (
	"encoding/json"
	"fmt"

	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/template"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
	"github.com/colony-2/c2j/pkg/workflow"
	"github.com/colony-2/jobdb/pkg/jobdb"
)

func setupPolicy() jobdb.RunPolicy {
	d := jobdb.Duration(workerops.SetupTimeout)
	return jobdb.RunPolicy{InvocationTimeout: &d}
}

func prepareInvocationTools(ctx workflow.Context, req workerops.ToolSetupRequest) (*workerops.ToolSetupResult, error) {
	input, err := jobdb.NewTaskData(req)
	if err != nil {
		return nil, err
	}
	output, err := ctx.DoTask(setupPolicy(), workerops.ToolSetupTaskType, input)
	if err != nil {
		return nil, fmt.Errorf("tool setup: %w", err)
	}
	b, err := output.GetData()
	if err != nil {
		return nil, err
	}
	var result workerops.ToolSetupResult
	if err = json.Unmarshal(b, &result); err != nil {
		return nil, err
	}
	if result.Diagnostics.Error != "" {
		return nil, fmt.Errorf("tool setup failed: %s", result.Diagnostics.Error)
	}
	return &result, nil
}

func resolveInvocationExtension(ctx workflow.Context, resCtx *template.ResolutionContext, selector string, opts extops.ResolveOptions) (*extops.ResolvedOp, coreops.RegisterableOp, int64, error) {
	if resCtx.Options.Mode == template.ModeValidate || resCtx.Options.LegacyExtensionResolution {
		resolved, op, err := loadSelectorOp(selector, opts)
		return resolved, op, 0, err
	}
	input, err := jobdb.NewTaskData(workerops.ExtensionResolutionRequest{Selector: selector, Options: opts})
	if err != nil {
		return nil, nil, 0, err
	}
	output, err := ctx.DoTask(setupPolicy(), workerops.ExtensionResolutionTaskType, input)
	if err != nil {
		return nil, nil, 0, err
	}
	b, err := output.GetData()
	if err != nil {
		return nil, nil, 0, err
	}
	var result workerops.ExtensionResolutionResult
	if err = json.Unmarshal(b, &result); err != nil {
		return nil, nil, 0, err
	}
	if result.Error != "" {
		return nil, nil, result.WallMS, fmt.Errorf("extension setup failed: %s", result.Error)
	}
	if result.Op == nil {
		return nil, nil, 0, fmt.Errorf("missing resolved extension manifest")
	}
	if key, ok, e := selectorRepoRefKey(selector, opts.RepositorySource, opts.RepositoryRef); e != nil {
		return nil, nil, 0, e
	} else if ok && opts.ResolvedRefs != nil && result.Op.ResolvedCommit != "" {
		opts.ResolvedRefs[key] = result.Op.ResolvedCommit
	}
	resolved, err := extops.RestoreResolvedOp(result.Op)
	if err != nil {
		return nil, nil, 0, err
	}
	op, ok := coreops.Get(extops.ExecutionOpType)
	if !ok {
		return nil, nil, 0, fmt.Errorf("extension execution op not registered")
	}
	return resolved, op, result.WallMS, nil
}
