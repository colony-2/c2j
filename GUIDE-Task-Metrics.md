# Task metrics

Tasks can report tagged counters, gauges and histograms without registering
instruments or adding recipe configuration. c2j keeps those mutations beside
each committed task result and reduces them for inspection. A series combines
a metric name with string labels, for example `llm_tokens_total` with
`provider`, `model` and `token_type` labels.

Metrics are available in recipe templates, persisted inspection and completion
snapshots. Existing fields called `metrics` in business outputs keep their meaning.
Successful recipe jobs automatically include a `c2j_metrics` snapshot beside
their recipe outputs. No output template is needed. This job-level field is
reserved; a recipe that declares an output with that name fails explicitly
instead of silently losing its value. Operation outputs can still use that name.

## Read metrics in recipes

`context.metrics` covers the job's committed reports through the current
consumption frontier. `scope.metrics` covers the current lexical scope occurrence,
including its failed attempts and catches. Published nodes expose
`sequence.<id>.metrics` or `states.<id>.metrics`; previous-run entries keep their
own occurrence's snapshot, such as `states.work.runs[0].metrics`.
Late-bound include nodes include their selection and capture preparation as well
as the selected body, including any ordinary hooks introduced by that body.

Each view has the same versioned snapshot shape as inspection. Its `series` map
contains arrays of labeled points, rather than an implicit sum of arbitrary
labels. Counter/gauge points have `value`; histogram points have `histogram`
with `count`, `sum`, `min`, `max` and `bucket_counts`. Integers remain exact in
CEL and Go templates. Guard missing names: absent measurements do not mean zero.

```yaml
outputs:
  tokens: '${{ "llm_tokens_total" in context.metrics.series ? context.metrics.series["llm_tokens_total"] : [] }}'
  preparation: '${{ "c2j_task_duration_seconds" in scope.metrics.series ? scope.metrics.series["c2j_task_duration_seconds"] : [] }}'
  previous_step: '${{ sequence.generate.metrics }}'
```

A running task sees earlier committed reports, excluding its own uncommitted
measurements. Output templates see committed descendant reports. A failed scope
does not erase resource consumption, even when its business output is discarded.
Synthetic catch outputs and final snapshots never become extra task mutations.
Node/run snapshots stay unchanged as later tasks finish. Restart context patches
can edit business output fields called `metrics`, but cannot edit derived views.
Validation and mocked test contexts expose empty series without measured zeros.

c2j consumes each accepted task-result event synchronously, before catches,
retry decisions and subsequent templates. It admits reports against one job-wide
canonical state before updating the job and active lexical scopes. Template reads
project these aggregates; they do not fetch or rescan execution history. Whole-job
retries share the invocation's accumulator. Resume, restart and replay allocate
fresh state and reconstruct it only as retained results are reached. Future and
discarded results never enter those views. Inspection and final completion use
explicit committed-history reads through the same admission rules.

## Emit from a native operation

Use the operation's context to find its task-local collector:

```go
metrics.FromContext(ctx).AddInt("llm_tokens_total", "{token}", map[string]string{
    "provider": "openai",
    "model": returnedModel,
    "token_type": "input",
}, inputTokens)
```

Import `github.com/colony-2/c2j/pkg/metrics`. `AddInt` adds a nonnegative
`int64` counter delta; `AddFloat` adds a floating-point counter delta.
`Observe` records a floating-point histogram observation. `Emit` accepts the
full `Mutation` structure for gauges, integer histograms and compact merges.
Calling these methods on an absent collector is a no-op. No new
`OpDependencies` method is required.

Collectors serialize concurrent emissions and coalesce counters, last gauge
sets and histogram populations. They retain bounded deltas rather than every
sample. A task step reports only its own work. Repeating an earlier step's
delta would count it again.

## Emit from an extension

Add a versioned `metrics` member to a result wrapper containing an object-valued `output`:

```json
{
  "output": {"answer": "example"},
  "metrics": {
    "version": 1,
    "mutations": [{
      "name": "llm_tokens_total",
      "kind": "counter",
      "number_type": "int64",
      "unit": "{token}",
      "labels": {"provider": "openai", "model": "returned-model", "token_type": "input"},
      "op": "add",
      "value": 1200
    }]
  }
}
```

On an ordinary nonzero exit, emit the complete wrapper with `output: null`.
c2j retains valid metrics and the original exit error, while failure artifacts
and objects are not published. Metrics captured before answer validation also
survive a schema failure. Missing, truncated or malformed failure stdout marks
the producer report unavailable. Execution-negotiation responses retain their
existing handoff behavior.

A plain output object with a field named `metrics` remains business data. Scalar,
array and null business fields named `output` also keep their original meaning
on success; they do not select the metric wrapper protocol.
Ordinary command stdout is also business data; it is not parsed as telemetry.
Automatic runtime reports remain separate from extension metadata, so an
invalid producer report cannot erase task timings.

## Instruments and limits

| Kind | Operation | Reduction |
| --- | --- | --- |
| `counter` | `add` | Sum finite, nonnegative deltas. |
| `gauge` | `set` | Last selected set in committed result order. |
| `histogram` | `observe` or `merge` | Sum counts, sums and buckets; merge observed minima/maxima. |

Each mutation names `number_type: int64` or `float64`. Integers remain exact,
including values above 2^53. An omitted unit means dimensionless. One name must
retain the same kind, numeric type, unit and histogram bounds throughout the
effective job history. Labels are unordered; an absent label differs from a
label with an empty value. The `c2j_` prefix is reserved for runtime measurements.

A compact histogram uses `op: merge` and a `delta` object containing `count`,
`sum`, `min`, `max` and `bucket_counts`. Omit `value`. Counts and bucket counts
are exact nonnegative integers. Every nonempty population supplies its observed
min/max; empty populations omit them and have zero sum/counts. Optional
`bounds` are finite, strictly increasing inclusive upper limits. Bucket counts
are noncumulative with one extra overflow bucket. Omitted bounds mean a single
bucket. Mean is total sum divided by total count; percentiles are not computed.

The wire follows OpenTelemetry delta sums, last values and explicit histogram
populations, with c2j identity and committed-history semantics. It does not
require a live SDK registry or exporter.

Reports are limited to 64 KiB, 256 mutations, 16 labels per mutation, 128 UTF-8
bytes per name/unit/label key, 256 bytes per label value, and 32 histogram bounds.
The effective history admits at most 10,000 series. Invalid shapes, descriptor
conflicts, secret-bearing values, arithmetic overflow or limit violations
reject the complete channel report and expose a bounded diagnostic. They do
not change the business outcome. Admission precedes query filtering, so a
filter cannot make a rejected report valid. Do not put prompts, paths, URLs,
credentials or unbounded identifiers into labels.

## Automatic measurements

Recipe workers emit these runtime metrics without producer opt-in:

| Name | Meaning and labels |
| --- | --- |
| `c2j_task_duration_seconds` | Histogram by `task_type`, `phase` (preparation/execution), and `outcome`. |
| `c2j_task_preparation_component_seconds` | Component histogram by `component`, `environment` and `outcome` (prepared/reused/failed/canceled). |
| `c2j_request_duration_seconds` | Histogram by `operation` (`git.pull`/`git.fetch`) and `outcome`. |
| `c2j_process_cpu_seconds_total` | Observed user+system CPU seconds of the worker process. |
| `c2j_process_cpu_observed_seconds_total` | Elapsed seconds measured alongside that CPU numerator. |

Preparation starts after execution admission and ends at body entry, including
input, configuration, secrets, workspace and environment work. Execution
continues through result processing and persistence preparation. Timings
exclude queueing, replay and the subsequent JobDB append. A preparation failure
has no execution observation. Component timings explain phases and may nest;
do not add them to task totals. Warm reuse records current lookup time.

The shared CPU sampler reads at task boundaries and about once per minute
while tasks are active. Only intervals with one active task receive CPU and
elapsed deltas. Overlap, resets, read errors and unsupported systems appear in
coverage. Divide combined CPU seconds by combined measured seconds to obtain
average busy cores: 30/30 plus 15/60 becomes 45/90, or 0.50 cores. This measures
the c2j process, including background work; it excludes subprocesses and is not
task-isolated or host utilization. Excluded task-window durations can overlap.

## Inspect committed history

```sh
c2j job metrics JOB --name llm_tokens_total
c2j job metrics JOB --through-ordinal 45 --deltas --json
c2j job metrics JOB --scope "$SCOPE" --attempt 2 --json
c2j job metrics JOB --task "$TASK" --output metrics.json
c2j job log JOB --task "$TASK" --json
```

Use native occurrence IDs from job inspection, not display paths or ordinals.
Filters compose. `--attempt` requires a task or scope. The inclusive cutoff
selects whole reports by physical result chapter ordinal; it cannot split a
long task's compact delta. Gauges and extrema are recomputed from selected
reports. JSON preserves operations, numeric types, labels, channels and
occurrence identities. Text displays labeled series, histogram means and
coverage. `--deltas` emits JSON; `--output` writes exact JSON to a new file.

Go callers use `jobinspect.API.Inspect` with `Operation: "metrics"` and
`Metrics: &metrics.Query{...}`. The task helper applies existing authentication,
tenant and lease boundaries without ambient-credential fallback. Incomplete
history returns an error together with its diagnostic result; the CLI writes
available diagnostics and exits unsuccessfully. Unsupported workers require
an upgrade. Ordinary listing does not scan every task's metric history.

Task inspection exposes metric metadata separately from business field
pointers. `job show` exposes a stored final snapshot where present. The shared
reducer counts each committed attempt/channel once, including failed retries
and caught failures. Replaying results does not add mutations. Effective
restart history includes retained work and the new suffix once. The inherited
versus new-work split is marked unavailable until authoritative provenance is
available. Related jobs and child jobs are not summed together.
If a parent returns a child's complete output as a business value, that value
includes the child's own `c2j_metrics`. The parent's automatic snapshot still
covers only the parent.

The last success chapter contains the job output, including `c2j_metrics` with
totals across all consumed operations and committed failed retries. The output
snapshot uses the synchronous accumulator, including bootstrap and hook work,
without reading history during recipe execution. Its cutoff is the last consumed
task result. `c2j job result JOB --field /c2j_metrics` reads that stored output.

The execution runtime also adds a versioned snapshot to existing c2j success/failure
final chapters, under `metadata.c2j_metrics`. It has the same series and coverage
as successful job output, with the completion cutoff and `final_chapter` source.
Failed jobs retain their error outcome and final metric metadata; they do not
produce a successful business result. Each snapshot records its cutoff, reducer
version, series and coverage. Uncertain
completion retries reuse their snapshot. A failed read records unavailable
coverage rather than complete zero totals; inspection can retry the history.
Cancellation inspection remains computed at an observed frontier and can
advance after late accepted writes. Frozen cancellation snapshots are pending
a separate decision and lifecycle support.

Measurements cover committed reports, not every physical execution or exact
billing. Missing reports and old histories mean unknown measurements, not zero.
No provider producer is bundled by this change; token fixtures demonstrate
the contract without provider calls.

## Upgrade and embedded wiring

Upgrade c2j readers, workers and task helpers together. New readers understand
version-1 activity envelopes and legacy internal results. New workers persist
version-2 envelopes; older workers cannot safely resume those histories.

Use `compiler.NewRecipeWorkerWithOptions` and the existing
[executionruntime wiring](pkg/executionruntime/README.md) for embedded workers,
with the same runtime's admission wrapper. The compiler instruments all its
registered activity and internal workers after applying caller decorators.
The runtime completion adapter stores final snapshots. Manually registered
external workers need explicit `metricsruntime.WrapWorker` wiring and a body
boundary; this does not instrument arbitrary third-party workers automatically.

`compiler.NewRecipeJobWorker` also installs the synchronous metric consumer
through JobDB v0.0.84's optional task-result observer factory. Custom
`ExecutionWorker`s that call `compiler.ExecuteRecipe` should set
`ObserveTaskResults: compiler.ObserveTaskMetrics`. Forward this optional worker
capability through decorators and replay overrides. Allocate state through the
factory for each invocation; do not also feed it through a replay observer or
asynchronous live listener. Custom workers without the factory expose empty
recipe views; their committed reports remain inspectable.
