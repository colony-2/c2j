package compiler

import (
	"context"
	"fmt"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
)

func isSelectorOp(opName string) bool {
	return extops.IsSelector(opName)
}

func loadSelectorOp(opName string, opts extops.ResolveOptions) (*extops.ResolvedOp, coreops.RegisterableOp, error) {
	resolved, err := extops.Resolve(context.Background(), opName, opts)
	if err != nil {
		return nil, nil, err
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
