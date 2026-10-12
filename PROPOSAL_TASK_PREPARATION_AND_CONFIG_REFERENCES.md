# Task preparation and configuration references

Implement this in c2j now, using existing JobDB task APIs. Target fresh databases
only; no compatibility path. See [CONFIGURATION.md](CONFIGURATION.md) for usage.

- Keep `${{ context.config.name }}` syntax, encoded as a structured JSON reference
  in durable inputs and hashes. Materialize a separate copy only inside a live
  task, using `TaskContext.GetConfiguration`. Validate resolved input types.
- JobDB owns configuration caching behind that existing API. c2j must not keep a
  cross-task config cache. Each execution, including resume, starts fresh;
  replayed task metadata never populates the live cache. c2j uses one snapshot
  per task and records only that task's used values and revision with its outcome.
- Permit whole-value input references and forwarding. Reject direct config use
  in state, `when`, selectors, work enumeration and other routing, including
  aliases. An explicit echo-style op can return a config value as ordinary output
  for subsequent routing; its recorded outcome preserves that decision.
- Resolve extension manifests and prepare tools/secrets inside the consuming
  task, after the outcome cache lookup. Record identities and setup durations
  with its outcome. Remove separate setup/resolution tasks.
- For now, preparation and execution share the task timeout. Move preparation
  into JobDB's separate preparation phase when available; that is a later change,
  not a prerequisite for this implementation.

Verify suspension after completed A: changing config must affect unfinished B,
while replay of A neither fetches configuration nor seeds B. Also verify stable
reference-based hashes, input validation, forbidden routing, recorded-output
routing, skipped setup on replay and mocked returns, and shared timeout handling.
