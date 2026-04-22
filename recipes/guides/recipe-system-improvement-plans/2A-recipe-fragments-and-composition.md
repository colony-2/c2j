# Plan 2A: Reusable Recipe Fragments (Compile-Time Composition)

## Goal

Encapsulate repeated workflow sub-patterns while keeping flows transparent.

## Problem pattern in current recipes

Large recipes duplicate near-identical structures:

- review checkpoint states with similar decision forms.
- cancel/update terminal paths.
- codex loop handling (`implement` + follow-up input + `implement_resume` + bug ticket fanout).

## Proposal

Introduce **fragments**: parameterized recipe snippets expanded at compile time into normal nodes/transitions.

## Fragment model

### Fragment definition

- Stored as versioned assets, e.g. `internal://fragments/review-gate@v1`.
- Defines:
  - required parameters
  - generated states/transitions
  - exposed outputs/artifacts.

### Recipe usage

```yaml
fragments:
  - id: pre_impl_gate
    use: internal://fragments/review-gate@v1
    with:
      title: "Pre-implementation review"
      continue_to: implement
      back_up_targets: [requirements_planning, implementation_planning, outcome_determination]
```

Expansion happens before validation/run so debugging still sees concrete nodes.

## Standard fragment candidates

1. `review-gate`
2. `cancel-route` (route + optional ticket update + terminal)
3. `completion-route` (route + ticket done update + terminal)
4. `codex-implementation-loop` (implement + follow-up + resume + bug ticket fanout)

## Recipe changes expected

- `new-ticket.yaml`:
  - replace `pre_implementation_review` and `pre_implementation_followup_review` with one fragment instantiation pattern.
  - replace cancel/completion boilerplate with shared fragments.
- keep behavior equivalent; generated nodes remain testable.

## Engine/runtime changes

1. Add fragment resolver in recipe compile pipeline.
2. Surface expanded node path mapping in job story:
   - `fragment_instance`
   - `expanded_node_path`.
3. Add `c2 recipe expand --file ...` command for review/debug.

## Migration plan

1. Implement one fragment (`cancel-route`) and migrate one recipe.
2. Add `review-gate` fragment and migrate `new-ticket`.
3. Add `codex-implementation-loop` fragment once stable.

## Compatibility and risks

- Compatibility: high if expansion output remains normal recipe graph.
- Risk: harder onboarding if fragment behavior is hidden.
- Mitigation: mandatory expansion preview + lint rule to pin fragment versions.

## Success criteria

1. `new-ticket.yaml` line count reduced materially without behavior changes.
2. `recipe-tests/new-ticket.scenario.md` stays stable against expanded node mappings.
3. Reviewers can inspect both source-with-fragments and expanded concrete graph.
