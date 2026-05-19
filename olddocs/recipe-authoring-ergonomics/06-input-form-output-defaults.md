# Requirement 6: Input Form Output Defaults

## Does The Spirit Make Sense?

Yes. Input forms already declare field shape; successful input outputs should be stable enough for recipes to read without per-field existence checks.

## Proposal

- Normalize `input.Output.Fields` against the rendered form schema before the input op output is committed to recipe state.
- Single-question inputs should also populate both `outputs.response` and `outputs.fields`.
- Implement normalization in a place that covers both real user input and recipe-test mocks. A compiler-level op output normalizer keyed by the `input` op, or a generic op output normalization hook, would cover more paths than only normalizing HTTP submissions.
- Add explicit form-field defaults to `FormField` as `default`, supported on every field type.
- Use default values by field type:
  - `short_answer`, `paragraph_text`, `date`, `time`: `""`;
  - boolean fields: `false`;
  - `checkboxes`: `[]`;
  - `multiple_choice`, `dropdown`: `""`;
  - `linear_scale`: the field's configured `scale.min`, unless an explicit field default is provided;
  - required fields: validate/preserve submitted value rather than silently inventing a value.

## Risks

- Boolean fields are in scope even though the current field type enum does not include a boolean type; implementation must add or map a boolean-capable field type.
- If normalization is implemented only in `Runtime.SubmitResponse`, mocked input outputs and auto-fill outputs can still lack defaults.
- Adding defaults for optional fields changes observable outputs for existing recipes.
- Required-field guarantees depend on the UI/API validation path; recipe-test mocks can bypass that unless tests normalize or validate mocks too.

## Clarifying Questions

Resolved.

## Decisions

- Include boolean field support in this requirement. Optional boolean fields default to `false`.
- Optional `multiple_choice` and `dropdown` fields default to `""`.
- Optional `linear_scale` fields default to the configured `scale.min`, unless the form schema provides an explicit `default`.
- Explicit form-field defaults are supported on every field type using the field name `default`.
- Single-question inputs populate both `outputs.response` and `outputs.fields`.
