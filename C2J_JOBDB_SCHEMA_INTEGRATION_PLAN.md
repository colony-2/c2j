# Plan: Always-On JobDB Schemas For c2j

## Goal

Every new c2j recipe job submits with the c2j JobDB schema. This is internal
behavior: no recipe opt-in, flags, config, or backwards-compat path.

The schema validates the chapter JSON c2j already persists so bugs in serialized
message shapes fail at submit, task append, or completion time.

## Dependency

Use `github.com/colony-2/jobdb v0.0.11` or newer. c2j needs:

- `SubmitJob.Schema`
- `SubmitRestartJob.Schema`
- `JobSchemaSelector`
- `JobSchemaRegistry`
- `JobSchemaHash`
- `ErrJobSchemaNotFound`
- `ErrJobSchemaArchived`
- `ErrJobSchemaValidation`

## Submit Behavior

Use one c2j-owned schema for every recipe job.

1. Compute the canonical schema hash with `jobdb.JobSchemaHash`.
2. Submit recipe jobs with `JobSchemaSelector{Hash: hash}`.
3. If JobDB reports that the hash is unknown, register the schema for that
   tenant and retry the submit with the hash.
4. Do not submit recipe jobs without a schema.

Restarts also carry the same schema hash. c2j restart helpers attach it before
calling JobDB; schema-aware engines register and retry on a hash miss.

## Runtime Wiring

`starter.StartRecipeJobWithOptions` and `starter.RestartRecipeJob` attach the
schema selector automatically.

Runtime/engine wrappers handle hash-miss registration:

- CLI runtime handles wrap submit engines.
- worker engines are wrapped too, so child recipe starts use the same behavior.
- runtime wrappers that sit between c2j and JobDB forward `JobSchemaRegistry`.

## Schema Scope

Keep a single schema, not one schema per recipe or per op set.

Schema the c2j-owned shapes tightly:

- `workflowctl.StartJob`
- `contextual.JobContext`
- `workerops.ActivityInvocationRequest`
- `task.OutputEnvelope`
- `workerops.ActivityInvocationOutput`
- `task.ContextPatch`
- root recipe source resolution messages
- within-recipe resolution messages
- JobDB task/job outcome wrappers

Keep arbitrary data open only where c2j intentionally accepts arbitrary recipe
or extension content:

- recipe `inputs`
- activity request `input`
- activity output `output`
- failure `attrs`
- context patch maps

Task attempts should validate `input` when JobDB exposes it. The schema still
allows known activity outcomes without top-level `input` because manual task
completion paths can persist visible chapters without the original input.

## Serialized Tags

Persisted c2j records should have explicit JSON tags. Old jobs are allowed to
stop replaying if their cached chapter shapes no longer match; new c2j jobs use
the tagged shape.
