# Requirement 1: Safe Path Access And Defaulting Helpers

## Does The Spirit Make Sense?

Yes. This is the lowest-risk way to remove repeated `in`, `has(...)`, direct dereference, and fallback boilerplate while preserving explicit CEL expressions.

## Proposal

- Prefer CEL-native optional access before adding custom path helpers.
- Enable cel-go optional syntax/types with `cel.OptionalTypes()` in the template resolver CEL environment and any legacy CEL environment still used for recipe expressions.
- Document native optional field and map access:
  - `obj.?field` returns an optional value.
  - `map[?key]` returns an optional value.
  - optional access is viral after the first optional segment, so `states.?review.outputs.fields.feedback` safely handles missing intermediate segments.
  - `.orValue(default)` provides defaulting.
  - `.hasValue()` and `has(obj.?field.path)` provide presence checks.
- Do not add `get(...)` as the primary path traversal primitive unless we decide string paths are still needed for dynamic path construction.
- Add `nonempty(value)` and `first_nonempty(values...)`, because native optional syntax handles presence/defaulting but non-empty fallback chains remain verbose.
- Define `nonempty(value)` as a boolean predicate:
  - false for absent optional values, `null`, empty strings after trimming whitespace, empty lists, and empty maps;
  - true for non-empty strings/lists/maps and for scalar values such as `false` and `0`.
- Define `first_nonempty(values...)` as returning the first argument that satisfies `nonempty(...)`, or `null` if none do.
- Authors should pass an explicit final fallback when the target field requires a non-null value.
- Register Go-template equivalents for `nonempty(...)` and `first_nonempty(...)`. Native optional CEL syntax does not apply to Go templates.
- Keep the selected value's native type unchanged. If a string field receives a map from `first_nonempty(...)`, the existing op input normalization and validation should still fail.
- Document that ordinary function calls such as `coalesce(...)` cannot protect unsafe direct references because CEL evaluates arguments before calling a function. Safe references should use optional access, `state_output(...)`, or `state_field(...)`.

## CEL-Native Findings

- The repo uses `github.com/google/cel-go v0.28.0`.
- cel-go supports optional field selection (`obj.?field`), optional map/list indexing (`map[?key]`, `list[?0]`), optional chaining, `.or(...)`, `.orValue(...)`, and `.hasValue()`.
- These features require `cel.OptionalTypes()` when constructing the CEL environment. The current template resolver environment does not include that option.
- `has(...)` is already native CEL and is useful, but it does not fully replace optional chaining. Plain `has(states.review.outputs.fields.feedback)` still depends on earlier path segments being safely reachable. With optional syntax, `has(states.?review.outputs.fields.feedback)` can test across a missing nested path.
- `optional.ofNonZeroValue(...)` can express non-empty-ish fallback, but its "zero value" semantics include `false` and `0` and it does not trim whitespace strings. That is close to, but not exactly the requested `nonempty(...)` semantics.

Example native form:

```yaml
commit_message: >-
  ${{
    states.?ready_to_merge_review.outputs.fields.commit_message
      .orValue(inputs.title)
  }}
```

Example native non-zero fallback, which is more verbose:

```yaml
upstream_repo: >-
  ${{
    optional.ofNonZeroValue(states.?ready_to_merge_review.outputs.fields.upstream_repo.orValue(""))
      .or(optional.ofNonZeroValue(inputs.?upstream_repo.orValue("")))
      .or(optional.ofNonZeroValue(context.?git.repo.orValue("")))
      .orValue("")
  }}
```

## Risks

- Enabling `cel.OptionalTypes()` changes the expression language accepted by recipes and should be covered by compile/run tests.
- Optional access makes recipes more CEL-native, but the syntax may be less familiar than `get(value, "path", default)`.
- Optional syntax is CEL-only; it does not help legacy Go-template-only expressions.
- Dotted `.?field` syntax cannot represent arbitrary map keys such as keys with dashes or dots; authors must use `map[?"literal-key"]` for those.
- `nonempty` semantics are intentionally different from CEL non-zero semantics: `false` and `0` are non-empty, whitespace-only strings are empty, and selected string values are returned unchanged.
- Validation mode currently avoids executing non-operator CEL calls in some paths. Helper names must still compile, but compile-only validation may not catch every runtime type issue.

## Clarifying Questions

Resolved.

## Decisions

- Use CEL optional syntax/types as the primary safe path access mechanism.
- Enable `cel.OptionalTypes()` in recipe expression environments.
- Do not include `get(...)` in the initial proposal unless a later requirement creates a clear need for dynamic string-path traversal.
- Add custom non-empty helpers because native optional non-zero fallback is verbose and has the wrong generic semantics for `0` and `false`.
- Name the helpers `nonempty(value)` and `first_nonempty(values...)`.
- `first_nonempty(values...)` returns `null` if no value qualifies. Authors should provide an explicit final fallback when needed.
- Whitespace-only strings count as empty.
- Register `nonempty(...)` and `first_nonempty(...)` in both CEL and Go-template function maps.
