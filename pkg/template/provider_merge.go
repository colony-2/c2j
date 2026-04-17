package template

import (
	"github.com/colony-2/c2j/pkg/template/funcregistry"
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

type compositeCELOptionsProvider struct {
	providers []CELOptionsProvider
}

func MergeCELOptionsProviders(providers ...CELOptionsProvider) CELOptionsProvider {
	filtered := make([]CELOptionsProvider, 0, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		filtered = append(filtered, provider)
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return compositeCELOptionsProvider{providers: filtered}
	}
}

func (c compositeCELOptionsProvider) TypeOptions() []cel.EnvOption {
	var opts []cel.EnvOption
	for _, provider := range c.providers {
		opts = append(opts, provider.TypeOptions()...)
	}
	return opts
}

func (c compositeCELOptionsProvider) FunctionOptions(adapter types.Adapter) ([]cel.EnvOption, error) {
	return c.FunctionOptionsWithContext(adapter, nil)
}

func (c compositeCELOptionsProvider) FunctionOptionsWithContext(adapter types.Adapter, ctxProvider funcregistry.ContextProvider) ([]cel.EnvOption, error) {
	var opts []cel.EnvOption
	for _, provider := range c.providers {
		if contextualProvider, ok := provider.(interface {
			FunctionOptionsWithContext(types.Adapter, funcregistry.ContextProvider) ([]cel.EnvOption, error)
		}); ok {
			extraOpts, err := contextualProvider.FunctionOptionsWithContext(adapter, ctxProvider)
			if err != nil {
				return nil, err
			}
			opts = append(opts, extraOpts...)
			continue
		}
		extraOpts, err := provider.FunctionOptions(adapter)
		if err != nil {
			return nil, err
		}
		opts = append(opts, extraOpts...)
	}
	return opts, nil
}

func (c compositeCELOptionsProvider) TemplateFuncsWithContext(ctxProvider funcregistry.ContextProvider) map[string]interface{} {
	out := map[string]interface{}{}
	for _, provider := range c.providers {
		templateProvider, ok := provider.(interface {
			TemplateFuncsWithContext(funcregistry.ContextProvider) map[string]interface{}
		})
		if !ok {
			continue
		}
		for name, fn := range templateProvider.TemplateFuncsWithContext(ctxProvider) {
			out[name] = fn
		}
	}
	return out
}
