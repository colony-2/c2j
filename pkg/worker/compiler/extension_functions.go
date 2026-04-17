package compiler

import (
	"context"
	"strings"

	"github.com/colony-2/c2j/pkg/contextual"
	"github.com/colony-2/c2j/pkg/recipe"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/template/extensionfuncs"
	"github.com/colony-2/c2j/pkg/template/funcregistry"
)

func recipeCELOptionsProvider(rec recipe.Recipe, execCtx contextual.JobContext, commitContext contextual.GitCommitContext, execOpts ExecutionOptions, base template.CELOptionsProvider) (template.CELOptionsProvider, error) {
	imports := resolvedFunctionImports(rec.GetMetdata().Extensions.Functions, execOpts.ResolvedSelectors)
	if len(imports) == 0 {
		return base, nil
	}

	resolveOpts := selectorLoadResolveOptions(execCtx, commitContext)
	importedProvider, err := extensionfuncs.BuildProvider(context.Background(), imports, extensionfuncs.BuildOptions{
		BaseDir:          normalizeExtensionBaseDir(resolveOpts.BaseDir),
		RepositorySource: strings.TrimSpace(resolveOpts.RepositorySource),
		RepositoryRef:    strings.TrimSpace(resolveOpts.RepositoryRef),
	})
	if err != nil {
		return nil, err
	}

	if base == nil {
		base = funcregistry.NewBuilder().WithDefaults()
	}
	return template.MergeCELOptionsProviders(base, importedProvider), nil
}

func resolvedFunctionImports(imports []recipe.ExtensionFunctionImport, pins map[string]string) []recipe.ExtensionFunctionImport {
	if len(imports) == 0 {
		return nil
	}
	out := make([]recipe.ExtensionFunctionImport, len(imports))
	copy(out, imports)
	for i := range out {
		out[i].Selector = resolvedSelector(out[i].Selector, pins)
	}
	return out
}

func normalizeExtensionBaseDir(path string) string {
	trimmed := strings.TrimSpace(path)
	switch trimmed {
	case "", contextual.WorktreePathSentinel, contextual.WorkdirPathSentinel:
		return ""
	default:
		return trimmed
	}
}
