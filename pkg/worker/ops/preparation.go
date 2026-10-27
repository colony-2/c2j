package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/colony-2/c2j/pkg/configuration"
	coreops "github.com/colony-2/c2j/pkg/ops"
	extops "github.com/colony-2/c2j/pkg/ops/extensions"
	"github.com/colony-2/c2j/pkg/permissions"
	"github.com/colony-2/c2j/pkg/secrets"
	"github.com/colony-2/c2j/pkg/template"
	"github.com/colony-2/c2j/pkg/toolenv"
)

// PreparationRequest contains identities, never resolved configuration or credentials.
// Preparation runs only inside a live task, under the same timeout as execution.
type PreparationRequest struct {
	Extension *ExtensionResolutionRequest `json:"extension,omitempty"`
	Scopes    []toolenv.Scope             `json:"scopes,omitempty"`
}

// IsZero makes nil and empty scope lists serialize identically when no extension
// needs preparation. omitempty alone does not omit a value-typed struct.
func (p PreparationRequest) IsZero() bool {
	return p.Extension == nil && len(p.Scopes) == 0
}

// ResolveInvocation loads a manifest and its defaults only for a live task.
func ResolveInvocation(ctx context.Context, inv *ActivityInvocationRequest) (ToolSetupRequest, error) {
	req := ToolSetupRequest{Scopes: append([]toolenv.Scope(nil), inv.Preparation.Scopes...)}
	if extension := inv.Preparation.Extension; extension != nil {
		start := time.Now()
		resolved, err := extops.Resolve(ctx, extension.Selector, extension.Options)
		if err != nil {
			return req, fmt.Errorf("extension setup failed: %w", err)
		}
		req.ResolutionMS = time.Since(start).Milliseconds()
		req.Extension = resolved
		inputs, _ := inv.Input["inputs"].(map[string]any)
		if len(extension.TemplateContext) > 0 {
			raw, err := json.Marshal(extension.RawInputs)
			if err != nil {
				return req, err
			}
			var withDefaults map[string]any
			if err := json.Unmarshal(raw, &withDefaults); err != nil {
				return req, err
			}
			if withDefaults == nil {
				withDefaults = map[string]any{}
			}
			if _, err := resolved.ApplyInvocationDefaults(withDefaults); err != nil {
				return req, err
			}
			value, err := resolveAddedDefaults(extension.TemplateContext, extension.RawInputs, withDefaults, inputs, true, func(value any) error {
				return authorizeDefaultSecrets(inv, resolved, value)
			})
			if err != nil {
				return req, err
			}
			inputs = value.(map[string]any)

		} else {
			if inputs == nil {
				inputs = map[string]any{}
			}
			raw, err := json.Marshal(inputs)
			if err != nil {
				return req, err
			}
			var original map[string]any
			if err := json.Unmarshal(raw, &original); err != nil {
				return req, err
			}
			if _, err := resolved.ApplyInvocationDefaults(inputs); err != nil {
				return req, err
			}
			value, err := resolveAddedDefaults(nil, original, inputs, inputs, true, func(value any) error { return authorizeDefaultSecrets(inv, resolved, value) })
			if err != nil {
				return req, err
			}
			inputs = value.(map[string]any)
		}
		inv.Input["inputs"] = inputs

		if len(resolved.Spec.Dependencies) > 0 {
			req.Scopes = append(req.Scopes, toolenv.Scope{ID: "op:" + extension.Selector, Packages: resolved.Spec.Dependencies})
		}
	}
	return req, nil
}

func PrepareInvocation(ctx context.Context, inv *ActivityInvocationRequest, prepareTools func(context.Context, ToolSetupRequest) ToolSetupResult) error {
	req, err := ResolveInvocation(ctx, inv)
	if err != nil {
		return err
	}
	return InstallInvocation(ctx, inv, req, prepareTools)
}

func InstallInvocation(ctx context.Context, inv *ActivityInvocationRequest, req ToolSetupRequest, prepareTools func(context.Context, ToolSetupRequest) ToolSetupResult) error {
	if err := validatePreparedInputs(inv, req); err != nil {
		return err
	}
	if len(req.Scopes) > 0 || req.Extension != nil {
		setup := prepareTools(ctx, req)
		inv.Setup = &setup
		if err := ctx.Err(); err != nil {
			return err
		}
		if setup.Diagnostics.Error != "" {
			return fmt.Errorf("tool setup failed: %s", setup.Diagnostics.Error)
		}
	}
	return nil
}

// Complete schema defaults before collecting or authorizing secret references.
func validatePreparedInputs(inv *ActivityInvocationRequest, req ToolSetupRequest) error {
	if req.Extension != nil {
		inputs, _ := inv.Input["inputs"].(map[string]any)
		defaulted, err := secrets.ApplySchemaDefaults(req.Extension.Spec.InputSchema, inputs)
		if err != nil {
			return err
		}
		inputs = defaulted.(map[string]any)
		inv.Input["inputs"] = inputs
		if err := req.Extension.ValidateInvocationInputs(inputs); err != nil {
			return fmt.Errorf("failed to validate selector inputs: %w", err)
		}
	}
	return nil
}

func materializeConfiguration(ctx context.Context, reg ActivityRegistration, inv *ActivityInvocationRequest, fetch configuration.Fetch) (*configuration.Diagnostics, error) {
	needsValidation := configuration.Contains(inv.Input)
	input, diag, err := configuration.MaterializeInput(ctx, reg.Step.InputType, inv.Input, fetch)
	if err != nil {
		return diag, err
	}
	inv.Input = input.(map[string]any)
	// Schema validation is deferred for references until their values are known.
	if needsValidation {
		if err := coreops.ValidateOpInputType(reg.Step.InputType, inv.Input, false); err != nil {
			return diag, err
		}
	}
	return diag, nil
}

// Only newly discovered defaults need evaluation. Supplied values were already
// evaluated in the compiler (possibly using custom CEL functions).
func resolveAddedDefaults(snapshot json.RawMessage, original, defaulted, resolved any, present bool, authorize ...func(any) error) (any, error) {
	if !present {
		value := defaulted
		if len(snapshot) > 0 {
			values, err := template.ResolveDeferredInputs(snapshot, map[string]any{"value": defaulted})
			if err != nil {
				return nil, err
			}
			value = values["value"]
		}
		for _, check := range authorize {
			if err := check(value); err != nil {
				return nil, err
			}
		}
		return value, nil
	}
	switch d := defaulted.(type) {
	case map[string]any:
		raw, ok := original.(map[string]any)
		if !ok && original != nil {
			return resolved, nil
		}
		current, _ := resolved.(map[string]any)
		out := make(map[string]any, len(d))
		for k, v := range current {
			out[k] = v
		}
		for k, v := range d {
			old, exists := raw[k]
			n, err := resolveAddedDefaults(snapshot, old, v, current[k], exists, authorize...)
			if err != nil {
				return nil, err
			}
			out[k] = n
		}
		return out, nil
	case []any:
		raw, ok := original.([]any)
		if !ok {
			return resolved, nil
		}
		current, ok := resolved.([]any)
		if !ok {
			return resolved, nil
		}
		out := append([]any(nil), current...)
		for i, v := range d {
			if i >= len(raw) || i >= len(out) {
				break
			}
			n, err := resolveAddedDefaults(snapshot, raw[i], v, current[i], true, authorize...)
			if err != nil {
				return nil, err
			}
			out[i] = n
		}
		return out, nil
	default:
		return resolved, nil
	}
}

func authorizeDefaultSecrets(inv *ActivityInvocationRequest, op *extops.ResolvedOp, value any) error {
	names, err := secrets.Names(value)
	if err != nil {
		return err
	}
	policy := permissions.State{Version: permissions.Version}
	if inv.Permissions != nil {
		policy = *inv.Permissions
	}
	trust := permissions.InputPolicy{Foreign: true}
	if policy.Inputs != nil {
		trust.RootRepository = policy.Inputs.RootRepository
		origin := op.ResolvedSelector
		if origin == "" {
			origin = op.SpecPath
		}
		root := permissions.RepositoryIdentity(trust.RootRepository)
		trust.Foreign = root == "" || root != permissions.RepositoryIdentity(origin)
	}
	// Include input allowances do not authorize defaults from another op repo.
	policy.Inputs = &trust
	for _, name := range names {
		if err := policy.CheckInput(name); err != nil {
			return fmt.Errorf("extension default: %w", err)
		}
	}
	inv.InputSecrets = append(inv.InputSecrets, names...)
	return nil
}
