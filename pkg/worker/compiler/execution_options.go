package compiler

import "github.com/colony-2/c2j/pkg/template"

type ExecutionMode string

const (
	ExecutionModeRun      ExecutionMode = "run"
	ExecutionModeValidate ExecutionMode = "validate"
)

type ValidationMode string

const (
	ValidateAll      ValidationMode = "all"
	ValidatePathOnly ValidationMode = "path_only"
)

type ValidationOptions struct {
	Mode       ValidationMode
	CollectAll bool
}

type ExecutionOptions struct {
	Mode       ExecutionMode
	Validation ValidationOptions
	// Optional CEL options provider to inject extra functions/types.
	CELOptionsProvider  template.CELOptionsProvider
	StateObserver       StateObserver
	DiagnosticsObserver template.DiagnosticsObserver
	// ResolvedSelectors carries internal selector pins for the current recipe run.
	ResolvedSelectors map[string]string
	// ResolvedGitRefs carries internal repo/ref pins for the current recipe run.
	ResolvedGitRefs map[string]string
}

func normalizeExecutionOptions(opts []ExecutionOptions) ExecutionOptions {
	if len(opts) == 0 {
		return ExecutionOptions{Mode: ExecutionModeRun}
	}

	out := opts[0]
	if out.Mode == "" {
		out.Mode = ExecutionModeRun
	}
	if out.Validation.Mode == "" {
		out.Validation.Mode = ValidateAll
	}
	out.ResolvedSelectors = cloneResolvedSelectors(out.ResolvedSelectors)
	out.ResolvedGitRefs = cloneResolvedGitRefs(out.ResolvedGitRefs)
	return out
}

func resolutionOptionsFromExecution(opts ExecutionOptions) template.ResolutionOptions {
	resolution := template.DefaultResolutionOptions()
	resolution.CELOptionsProvider = opts.CELOptionsProvider
	resolution.ResolvedSelectors = cloneResolvedSelectors(opts.ResolvedSelectors)
	resolution.ResolvedGitRefs = cloneResolvedGitRefs(opts.ResolvedGitRefs)
	resolution.DiagnosticsObserver = opts.DiagnosticsObserver
	if opts.Mode == ExecutionModeValidate {
		resolution.Mode = template.ModeValidate
		resolution.ClampSliceIndex = true
		resolution.AllowFutureStepRefs = true
		resolution.ValidationMode = string(opts.Validation.Mode)
	} else {
		resolution.Mode = template.ModeRun
	}
	return resolution
}
