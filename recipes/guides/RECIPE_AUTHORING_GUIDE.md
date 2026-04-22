# Recipe Authoring Guide

This guide explains how to write Colony2 recipes, discover available operations, and apply the scoping and templating rules that govern recipe execution.

## 1. Inspect the Schema and Operations

1. Generate the full recipe JSON Schema (useful for editors and validation). Must be run in server/cortex:
   ```bash
   go run ./cmd/cortex schema
   ```
2. Validate a recipe file against the schema before executing it:
   ```bash
   go run ./cmd/cortex validate path/to/recipe.yaml
   ```
3. Review the operation catalog (inputs, outputs, semantics) in `server/cortex/docs/RECIPE_OPS_REFERENCE.md`.

## 2. Core Node Types

Recipes are composed of three node types:

- **`op`** – invokes a single activity/inline op.
- **`sequence`** – executes child nodes in order.
- **`state`** – models a state machine with transitions.

Each node type introduces a scope boundary. Understanding what inputs/outputs are visible inside and outside that boundary is crucial for referencing data correctly. See the **Node Scope Specification** (`server/recipe-worker/docs/NODE_SCOPE_SPEC.md`) for detailed rules and examples.

## 3. Referencing Data with Templates

Use the templating features summarised in `TEMPLATE_REFERENCE_CHEATSHEET.md` to reference inputs and outputs. Key patterns:

- Inside a sequence: `sequence.<step-id>.outputs.field`
- Inside a state machine: `states.<state-id>.outputs.field`
- Export values via the enclosing node’s `outputs:` block so outer nodes can use them.

## 4. Authoring Flow

1. **Plan the structure** – decide where to use `sequence` vs. `state`. Export any data that needs to flow outward with an `outputs:` block.
2. **Select ops** – consult `RECIPE_OPS_REFERENCE.md` and the generated schema for required inputs/outputs.
3. **Wire references** – follow the scope rules and templating patterns when referencing prior node outputs.
4. **Validate** – run `cortex validate` to catch structural issues.
5. **Execute or test** – use `go run ./cmd/cortex execute …` or the integration harness under `server/recipe-worker/test-fixtures` to exercise the recipe end-to-end.

## 5. Additional References

- Operation reference: `server/cortex/docs/RECIPE_OPS_REFERENCE.md`
- Node scope rules: `server/recipe-worker/docs/NODE_SCOPE_SPEC.md`
- State machines: `STATE_MACHINE_GUIDE.md`
- Sequences: `SEQUENCE_GUIDE.md`
- Template syntax/examples: `server/recipe-worker/test-fixtures/TEMPLATE_REFERENCE_CHEATSHEET.md`

Keep these resources handy while authoring recipes to ensure consistent, valid workflows.
