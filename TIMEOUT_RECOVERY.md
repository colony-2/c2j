# Recipe timeout recovery

c2j records timeout scope entry in the job's durable task history. Replaying a
recipe reuses that timestamp rather than starting a fresh wall-clock budget.
This applies to sequence, state-machine, and operation timeout envelopes,
including operation defaults and recipes resolved by the worker.

## Behavior

- A scope's deadline is its recorded first-entry time plus its configured
  timeout. Time spent suspended or waiting for another executor consumes that
  budget once the scope has been entered.
- Runtime-resolved recipes record their scope after resolution, when execution
  enters the recipe. The initial JobDB policy is not rewritten. Any separate
  JobDB job-level limit still applies; preloaded root timeouts retain their
  existing submission-time job-level limit.
- Nested scopes cannot grant work more time than an enclosing scope allows.
- Completed task results can be replayed after their deadline. A completed
  nested scope does not prevent later work outside that scope.
- A new task admission after an active scope expires produces a non-retryable
  total-timeout error. An unhandled error is recorded as terminal `TIMEOUT`,
  with completion status `failed_timeout` and lifecycle status `COMPLETED`.
- The same error remains identifiable as `context.DeadlineExceeded` inside
  c2j. Recipe failure handling can identify it as a timeout.

## Implementation and embedding

`recipe_timeout_checkpoint` is an internal control task. One checkpoint records
each scope entry; another records admission to a task within timed scopes.
Nested scopes share that admission timestamp. Existing JobDB task caching
preserves these records across recovery, independently of client payload and
execution-requirement handoffs.

The admission record lets replay distinguish an old task dispatch from new
work. c2j passes the stable remaining budget into JobDB's task policy. JobDB
continues to own actual task execution, caching, external-task scheduling, and
its own deadline enforcement.

The standard recipe worker registers the control task automatically, outside
execution-resource guards. Hosts that construct `NewRecipeJobWorker` and
register task workers individually must also register
`compiler.NewTimeoutCheckpointTaskWorker()`.

Validation and the local recipe-test harness execute bookkeeping locally; it
does not count as an application operation and needs no op mock. Control
checkpoints do not attach or replace forwarded Git thinpacks.

## Deployment compatibility

This changes timed recipes' chapter sequence and the c2j job schema. Deploy
matching c2j workers and submit fresh jobs. Existing histories without these
checkpoints and jobs pinned to the previous schema are not migrated by this
change. Do not resume those histories under the new worker expecting transparent
compatibility. No existing data is deleted or rewritten automatically.

Timed input retrieval supports the checkpoint format introduced in v0.0.58.
The c2j workflow adapter follows the admission checkpoint's recorded input
reference to the exact successful `input:generate_form` result, preserving its
form, artifacts, Git snapshot, and workspace context. It rejects invalid
references and unrelated task outputs instead of searching for an older form.
The original JobDB task handle still completes the wait with its original
ordinal and input hash. This retrieval fix does not change checkpoint inputs,
outputs, schema, or deadline calculations; existing checkpoint histories remain
readable. It does not migrate histories from before checkpoints were introduced.

The schema also now accepts JobDB's structured timeout payload for both task
and final outcomes. Previously it incorrectly required a string, preventing
typed timeout completion.

## Remaining JobDB limitations

The pinned JobDB v0.0.23 already fixes external-task deadline ordering and
terminal timeout recording after existing task history. An unfinished external
task wakes for the earliest applicable deadline and records a timeout instead
of repeatedly handing off. c2j's durable scope checkpoints provide the stable
remaining budget used by that behavior. This change does not edit JobDB.

Dependency waits remain different: `AwaitJobs` does not publish a deadline
wake-up, and an alternate route does not bypass unresolved dependencies or a
future `WaitUntil`. A scope budget is enforced when execution resumes, but these
changes do not guarantee deadline-driven acquisition while dependencies remain
blocked. See [the timeout investigation](EXTERNAL_INPUT_TIMEOUT_INVESTIGATION.md)
for current verification and the separate scheduling requirement.

## Tests

```bash
GOTOOLCHAIN=go1.26.1 go test ./pkg/worker/compiler \
  -run 'TestRecipeDeadlineAcrossRecovery|TestDurableTimeout|TestNestedTimeout|TestC2JSchemaAcceptsTimeout|TestExpiredClamp' -count=1
GOTOOLCHAIN=go1.26.1 go test -tags=timeoutrepro ./pkg/worker/compiler \
  -run TestTimeoutRepro -v -count=1
```

The first command checks c2j regressions and controls on toy, SQLite, and
SQLite-over-HTTP, including successful terminal timeout for preloaded roots.
The second command checks JobDB v0.0.23's external-task deadline handling and
history preservation, plus characterizations of the remaining dependency-wait
limitations. Assertions for the two fixed upstream bugs now require the correct
behavior; only dependency-wait characterizations describe an outstanding gap.
