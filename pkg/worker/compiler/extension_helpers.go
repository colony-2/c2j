package compiler

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/template"
	workerops "github.com/colony-2/c2j/pkg/worker/ops"
)

func isSelectorOp(opName string) bool {
	return extops.IsSelector(opName)
}

func loadSelectorOp(opName string, opts extops.ResolveOptions) (*extops.ResolvedOp, coreops.RegisterableOp, error) {
	resolved, err := extops.Resolve(context.Background(), opName, opts)
	if err != nil {
		return nil, nil, err
	}
	if key, ok, err := selectorRepoRefKey(opName, opts.RepositorySource, opts.RepositoryRef); err != nil {
		return nil, nil, err
	} else if ok && opts.ResolvedRefs != nil && strings.TrimSpace(resolved.ResolvedCommit) != "" {
		opts.ResolvedRefs[key] = strings.TrimSpace(resolved.ResolvedCommit)
	}
	registeredOp, exists := coreops.Get(extops.ExecutionOpType)
	if !exists {
		return nil, nil, fmt.Errorf("operation %s not found", extops.ExecutionOpType)
	}
	return resolved, registeredOp, nil
}

func selectorInvocationInput(opName string, inputs map[string]interface{}, repoSource string, repoRef string) map[string]interface{} {
	return map[string]interface{}{
		"selector":          opName,
		"inputs":            inputs,
		"repository_source": repoSource,
		"repository_ref":    repoRef,
	}
}

func resolvedSelector(selector string, pins map[string]string) string {
	selector = strings.TrimSpace(selector)
	if selector == "" || len(pins) == 0 {
		return selector
	}
	if pinned, ok := pins[selector]; ok && strings.TrimSpace(pinned) != "" {
		return strings.TrimSpace(pinned)
	}
	return selector
}

func selectorLoadResolveOptions(execCtx contextual.JobContext, commitContext contextual.GitCommitContext) extops.ResolveOptions {
	if repoSource, repoRef := selectorDeclaredRepository(execCtx); repoSource != "" && repoRef != "" {
		return extops.ResolveOptions{
			RepositorySource: repoSource,
			RepositoryRef:    repoRef,
		}
	}

	if baseDir := normalizeExtensionBaseDir(execCtx.Environment.WorktreePath); baseDir != "" {
		return extops.ResolveOptions{BaseDir: baseDir}
	}

	repoSource := strings.TrimSpace(execCtx.GitBase.BaseRepo)
	repoRef := strings.TrimSpace(execCtx.GitBase.ResolvedBaseHash)
	if repoRef == "" {
		repoRef = strings.TrimSpace(commitContext.ParentRef)
	}
	if repoRef == "" {
		repoRef = strings.TrimSpace(execCtx.GitBase.BaseRef)
	}
	return extops.ResolveOptions{
		RepositorySource: repoSource,
		RepositoryRef:    repoRef,
	}
}

func selectorInvocationRepository(execCtx contextual.TaskExecutionContext) (string, string) {
	return selectorDeclaredRepository(execCtx.JobContext())
}

func selectorDeclaredRepository(execCtx contextual.JobContext) (string, string) {
	repoSource := strings.TrimSpace(execCtx.RecipeSource.Repo)
	repoRef := strings.TrimSpace(execCtx.RecipeSource.Ref)
	if repoSource == "" || repoRef == "" {
		return "", ""
	}
	return repoSource, repoRef
}

// resolveOptionalSelectorInputs preserves which expressions supplied values for
// the worker's deferred default merge. Validation applies defaults immediately.
func resolveOptionalSelectorInputs(
	resCtx *template.ResolutionContext,
	expressions map[string]string,
	declared map[string]any,
	request *workerops.ExtensionResolutionRequest,
	validationOp *extops.ResolvedOp,
) (map[string]any, *workerops.ExtensionResolutionRequest, error) {
	present, err := resCtx.ResolveOptionalInputs(expressions)
	if err != nil {
		return nil, nil, err
	}

	prepared := *request
	prepared.RawInputs = maps.Clone(declared)
	for key := range present {
		prepared.RawInputs[key] = expressions[key]
	}
	if validationOp != nil {
		maps.Copy(declared, prepared.RawInputs)
		if _, err := validationOp.ApplyInvocationDefaults(declared); err != nil {
			return nil, nil, err
		}
		for key := range present {
			delete(declared, key)
		}
	}
	return present, &prepared, nil
}
