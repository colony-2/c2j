package compiler

import (
	"context"
	"fmt"

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
