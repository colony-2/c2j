# Native recipe test suites

Run a self-contained suite:

```sh
c2j test validate --file recipe-tests/build.test.yaml
c2j test run --file recipe-tests/build.test.yaml
```

Each suite declares its recipe target. Local paths resolve relative to the
suite file, regardless of the current working directory. An explicit
`--recipe-file` or `--recipe` overrides the declaration for single-file runs.

```yaml
recipe: ../build.yaml
cases:
  - id: rejected-plan-does-not-merge
    type: recipe_case
    inputs:
      prompt: Improve the requested behavior
    # Supply the op mocks appropriate to this recipe here.
    assertions:
      - type: output_equals
        path: merged
        value: false
```

The example illustrates the format; a real mocked case must supply its recipe's
op responses. Existing case, mock and assertion formats remain supported.

Discover suites recursively without a manifest or runner script:

```sh
c2j test validate --directory recipe-tests
c2j test run --directory recipe-tests --out-dir .c2/test-results
c2j test run --directory recipe-tests --case rejected-plan-does-not-merge
```

Discovery recognizes `*.test.yaml`, `*.test.yml`, `*.test.json`, and
`*.scenario.md`. Markdown suites contain their declaration in a fenced YAML or
JSON block. Hidden directories, `vendor`, `node_modules`, and symlinks are not
scanned. Files run in sorted path order. Every discovered suite must declare a
recipe and contain cases with nonempty, unique IDs within that suite.

Malformed suites fail the run; other suites are still attempted unless
`--stop-on-failure` (run) or `--fail-fast` (validate) is requested. No discovered
suites or no selected cases is an error. `--directory` cannot be combined with
an explicit recipe target, `--file`, `--stdin`, or `--format`.

Suites requiring live services declare `live: true` at the top level. Directory
runs report these as `excluded_live` unless `--include-live` is supplied.
Single-file execution of a live suite also requires that flag. This declaration
is a selection rule, not a network sandbox; mock external ops in offline cases.

Directory runs write an aggregate `summary.json`, including setup and parse
errors, plus each executed suite's ordinary case reports under
`suites/<relative-suite-path>/`. Case IDs may repeat across suites without
overwriting results. Single-file commands and their result formats are retained.

## Runtime-backed cases

Add `runtime: {}` to run a case through JobDB and the normal recipe workers.
Without it, existing cases retain the fast mock executor, including older cases
labelled `integration_case`. Each runtime case has its own temporary database
and Git repositories; no standalone server or Go project is required.

For recipes with fixed execution requirements, `runtime.allocation` can provide
synthetic facts to the case-local admission guard, for example
`{schema_version: 2, base: ubuntu-26.04-amd64}`. Facts use the normal allocation
validation rules; a wildcard is not an actual base. Reports identify these facts
as `fixture_allocation`. This fixture does not provision an executor, change the
host architecture or isolate passthrough commands. Use it for control-flow and
reporting tests; it cannot certify native execution or worker readiness.

```yaml
recipe: ../recipes/write-and-review.yaml
cases:
  - id: accept-design
    type: integration_case
    runtime:
      responses:
        - node_path: write-and-review/approve
          fields: {decision: approve}
          attachments: {annotation: fixtures/annotated-design.md}
    mocks:
      ops:
        - match: {node_path: write-and-review/draft}
          behavior:
            mode: return
            outputs: {success: true}
            artifacts: {design.md: '# Design'}
            effects:
              worktree: {design.md: '# Design'}
              objects:
                session:
                  type: fixture.session/v1
                  metadata: {session_id: example}
    assertions:
      - {type: output_equals, path: decision, value: approve}
      - {type: review_document_exists, node_path: write-and-review/approve, path: design}
```

The recipe must contain those authored node IDs and accept the declared review
fields. Responses are submitted through the input API, which validates fields,
attachments and request identity. Attachments use existing file-upload fields.

Optional tenant fixtures can be supplied without changing the recipe:

```yaml
runtime:
  secrets:
    codex_api_key: synthetic-test-credential
  config:
    codex_api_base_url: https://fixture.invalid
```

Omit these maps to test missing settings. Optional secret defaults also work for
mocked extension ops: normal preparation uses the empty fixture provider, then
the mock supplies the result without invoking the model. Real provider outages
remain failures in production.

Runtime fixture vocabulary:

- `runtime.cell` selects the root cell (default `root`). `runtime.cells` maps
  names to `files` (path to inline text) and `file_sources` (destination to source
  file). Sources resolve relative to the suite; destinations stay in the fixture.
- `runtime.secrets` maps secret names to synthetic string values; `runtime.config`
  maps configuration names to JSON values. Each case has its own provider and
  configuration store, shared with its child jobs. Both are empty by default,
  so absent optional settings use their normal fallbacks. Required missing
  secrets still fail before a mock executes. Tests never read production tenant
  credentials. Use fake values only; these fixture declarations live in the suite.
  Materialized secrets are masked in runtime call reports; configuration remains
  visible. Extension mock inputs retain secret references for forwarding.
- Existing op mocks support `return`, `fail`, and `passthrough`. `match.cell`
  scopes a response to a cell. Real built-in orchestration runs normally;
  commands and extensions require a matching response or explicit passthrough.
- `behavior.effects.worktree` writes files before the normal snapshot step.
  `artifact_files` reads fixture files into ordinary output artifacts.
- `behavior.effects.objects` maps an output field to `type`, `metadata`, and
  optional `files` (part name to fixture source). c2j publishes real immutable
  checkpoints. No fabricated object keys are needed.
- `behavior.effects.children` contains `recipe`, `cell`, and optional `inputs`.
  Recipes resolve relative to the suite and are submitted through the running
  op's child broker. Normal workers execute the children and the parent receives
  ordinary `jobs` metadata. Declare target cells in `runtime.cells`.
- `runtime.responses` supplies ordered answers matched by `node_path` and
  optionally `cell`. Use `fields` for forms/reviews, `response` for structured
  input, and `attachments` for file-upload fields.
- `runtime.expect_error` requires execution to fail with the specified text.
  Unexpected success and timeouts do not satisfy it.

Unused mocks/responses fail runtime cases. Case timeouts include child execution
and waiting for input. All workers stop before the disposable runtime is removed.

## Per-response synthetic actors

Each native response can select its synthetic submission actor, beside `fields`,
`response` and `attachments`:

```yaml
runtime:
  responses:
    - node_path: approval/review
      actor: {id: synthetic-reviewer, kind: human}
      fields: {decision: approve}
```

For structured input, use `response: {decision: approve}` instead of `fields`.
Form file uploads still use `attachments: {annotation: fixtures/feedback.md}`
with a real fixture file and a matching file-upload field. Actor selection keeps
native answer validation, attachment binding and generated receipts. It does not
add structured attachment syntax or change production authentication or rules.

Omitting `actor` selects `{id: recipe-test, kind: test}` for that response only.
Use `{id: synthetic-agent, kind: automation}` to exercise an automation receipt.
A human-only recipe gate can distinguish these actors using the native receipt;
valid test and automation answers remain ineligible for that gate.

A supplied actor must be an object containing exactly `id` and `kind`, both
nonblank strings. Null actors, scalars, arrays, unknown members, missing members,
null members, non-string members, empty strings and whitespace-only strings fail
fixture validation before execution. Arbitrary nonblank kinds are accepted;
values retain their exact spelling and surrounding whitespace. Answer fields
named `actor` are answer data and cannot select the receipt actor. Overrides
never carry into another response or case. Direct Go fixtures use
`Actor: &input.Actor{ID: "synthetic-reviewer", Kind: "human"}`; nil means omission.

The runnable form and structured suites below assert human eligibility, default
and automation ineligibility, explicit test receipts, and equality between the
reported receipt and the persisted input output:

```bash
c2j test run --file testdata/repros/native-response-actors/form.test.yaml --out-dir /tmp/native-actors-form
c2j test run --file testdata/repros/native-response-actors/structured.test.yaml --out-dir /tmp/native-actors-structured
```

These examples require the per-response actor implementation: older builds ignore
the unknown fixture member and still submit as `test`.

## Assertions about recipe behavior

`op_input_equals` checks the last call's input at `path`; `op_call_count` counts
calls to `node_path`. `review_document_exists` checks a named document on an
answered review. Reports include captured calls and review forms.

Use `cel_true` for more detailed checks without rewriting the recipe:

```yaml
assertions:
  - type: cel_true
    expr: '!outputs.merged && !calls.exists(c, c.op == "squashrebasemerge")'
  - type: cel_true
    expr: 'calls.filter(c, c.op == "extension_execution").all(c, c.inputs.prompt.contains("requested behavior"))'
```

CEL assertions receive `outputs`, `calls`, `reviews`, `artifacts` (text by name),
and execution `status`. They must evaluate to boolean true; invalid expressions
and evaluation errors fail. Op-call assertions also work in the fast executor.

SQLite scenarios below support cooperative worker stop/join and runtime reopen.
Lease fault injection, snapshot encoding and wire protocol tests remain Go regressions.

Runtime mocks may match `selector` (the authored selector or resolved revision)
plus `cell` and `node_path`. Set `repeat: true` on a mock to reuse it, for example
a real schema gate. Ordered one-shot mocks remain the default; a required mock
that is never used fails the case. Calls record small input artifact contents
in `calls[].artifacts` for feedback/document-routing assertions.

Passthrough commands run in the worker environment inside disposable test
worktrees, with normal snapshots. The former `runtime.command_sandbox` override
has been removed. Provision any isolation around the worker externally.
`cells.<cell>.file_sources` accepts file or directory sources relative to the
suite. Each imported regular file gets ordinary `0644` permissions plus its
source execute bits (`0644 | (source_mode & 0111)`), for both direct files and
directory members. These permissions are applied explicitly, regardless of umask
or an existing destination's permissions: nonexecutable imports clear old execute
bits, and executable imports set the source execute mask. Other source permissions
and special bits are not copied. Imports happen before the initial fixture Git
commit, so a source script with mode `0755` starts with committed mode `100755`
and can run directly without dirtying the worktree. Git records only executable
versus nonexecutable state, not individual permission bits. Inline `files` and
mock-effect writes retain their existing ordinary-file behavior. Directory trees
exclude `.git` directories and
reject symlinks; imported files must be regular files, and destination confinement and
symlink protections still apply.

A case-level `expect_error: "substring"` expresses an expected execution failure
in either executor. Unexpected success, unrelated failures, validation failures,
and case timeouts still fail the test. Prefer this to a shell wrapper that
accepts any nonzero exit code. `runtime.expect_error` remains supported.

Runtime CEL assertions also receive `repositories`, keyed by fixture cell. Each
entry contains `head`, `new_commits` since the seeded commit, `clean`,
`changed_files`, and `files` (contents of changed regular files up to 64 KiB).
This checks what a recipe actually merged, including that consultation-only
cells remain unchanged, without inspecting temporary directories in a script.

For branch-specific mock cases, `options.validation_mode: path_only` validates
the path selected by that case's inputs and replies. The default `all` retains
whole-graph validation. Use `path_only` when an unselected branch requires data
that is intentionally absent (for example, a resumed session in a fresh-session
case). `run` still executes the complete selected scenario and its assertions.

`options.validation_mode: structure_only` checks declaration structure and source
resolution, as runtime cases do. It emits an `execution_validation_deferred`
warning. Use it for data-dependent mock suites whose artifact/history expressions
cannot evaluate against static placeholders. Their behavior must be checked with
`run`; this option does not bypass any run-time validation or assertion.

For assertions about state at an invocation, declare `runtime.observe_files`
with cell-relative file paths. `calls[].worktree` records those regular files
before each op; missing files are absent from the map. Observations are limited
to 64 KiB per file and cannot follow symlinks outside the disposable worktree.
Use this to check that a candidate survives a consultation and foreign edits do
not enter the next workspace. `calls[].outputs` records successful op outputs,
including opaque checkpoint references, so a test can compare the next input
session to the exact previous output instead of checking only its type.

```yaml
runtime:
  observe_files: [candidate.txt, experiment.txt]
assertions:
- type: cel_true
  expr: 'calls.filter(c, c.op == "extension_execution")[1].inputs.inputs.session == calls.filter(c, c.op == "extension_execution")[0].outputs.session'
```

## Accepted composite and transition observations

Native cases can opt into selected intermediate values, including when the root
later fails. This is a local test facility; persisted job inspection cannot
recover values that an older runner did not record. Add `runtime.observe` to the
suite, without modifying the production recipe:

```yaml
runtime:
  observe:
    scopes:
      - node_path: build/develop/verify/sequence/adaptive-verify
        outputs: [ok, allowed, result, artifact_refs, agent_artifacts]
        retain_artifacts: [diagnostic.txt]
      - node_path: build/develop/verify
        outputs: [ok, allowed, result, artifact_refs, agent_artifacts]
    transitions:
      - {machine_path: build/develop, from: verify, to: dispatch}
    entries:
      - {machine_path: build/develop, state: dispatch}
    limits: {records: 256, bytes: 1048576}
```

Paths match exact resolver invocation paths, including implicit `sequence` nodes;
use the paths from existing op-call reports and the recipe's nesting. Each
selector can include `cell`. Wildcards and empty selectors are rejected.
`outputs` selects top-level output keys. `retain_artifacts` selects names from
that completion's bound artifact references, not arbitrary file paths or URLs.

Run the included, offline example:

```sh
c2j test validate --file testdata/observations/bridge.test.yaml
c2j test run --file testdata/observations/bridge.test.yaml --case bounded-observation --out-dir /tmp/observation-report
```

The example deliberately fails the root after dispatch entry. Its expected-error
match and intermediate assertions must all pass. It demonstrates the reporting
capability; it is not the retained recipes consumer acceptance suite.

CLI results store observations in
`cases/<case-id>/result.json` under `run.runtime`. The Go harness returns the same
object as `CaseRunResult.Runtime`. Version 1 adds:

| Field | Meaning |
| --- | --- |
| `observations_version` | Observation schema version, currently 1. |
| `observations` | Immutable, redacted records in capture order. |
| `observation_collection` | Complete flag, effective limits, retained count/bytes, dropped count, reason totals and bounded omission diagnostics. |
| `observation_artifacts` | Diagnostic manifests with observation ID, typed ref, retention status, size and optional retrieval path. |

Every record contains `id`, `order`, `kind`, `case_id`, `job_id`, `cell`,
`identity_kind`, `scope_id`, `parent_scope_id`, `scope_visit`, `callback_attempt`,
`node_path`, `invocation` (resolver hash/path/sequence), `machine_path`,
`machine_scope_id`, and `state_scope_id` where a containing state exists.

`identity_kind: test_local_scope_callback` explicitly identifies the test-local
full occurrence path. Each parent counts scope calls per site; each scope counts
body and catch callback entries. Together with job ID, this distinguishes visits,
retries and nested machines. These counters are not JobDB native scope IDs or
native attempt numbers and cannot be passed to `job log --task`.

`scope_completed` records only accepted outcomes after existing publication or
forwarding succeeds. It contains `outputs` and `output_presence`; a missing key
has presence false, while null, false and `{}` remain distinct literal values.
Acceptance belongs to that scope alone. Require a separate enclosing-state
completion before claiming that state accepted the child's result.

`transition_selected` contains the actual rendered `from`, `to`, `payload` and
accepted `source_scope_id`. `route_selected` identifies catch routing, includes
`failure`, and has an empty accepted source ID. `state_entered` contains the
actual installed `incoming_transition`, `transition_id` and source scope ID.
Entries without a selected matching transition have empty link fields. Configure
both transition and entry selectors when requiring linkage. Initial-state entry
can be observed, but this interface selects only transitions with named sources.

CEL assertions receive `observations` and `observation_collection` using the
same sanitized JSON values. Require complete collection, exact counts and linked
identities; checking a Boolean alone cannot establish an execution chain:

```yaml
assertions:
  - type: cel_true
    expr: 'observation_collection.complete && observations.filter(o, o.kind == "transition_selected").size() == 1'
  - type: cel_true
    expr: 'observations.filter(o, o.kind == "state_entered").all(e, e.transition_id != "" && e.source_scope_id != "")'
```

Without observation configuration, the new report fields are absent and CEL
receives an empty observation list with `complete: false`. Observation capture
errors produce diagnostics; they do not change scope acceptance, catches,
retries, gate results or worker errors. Assertions requiring completeness fail
when records or selected diagnostics are omitted.

Default limits are 256 records and 1 MiB of encoded records, manifests and
retained diagnostic bytes per case, shared across child jobs. Hard maxima are
4096 records and 8 MiB. Occurrence paths are capped at 8192 bytes; longer identities are explicitly omitted. Each event is limited to 256 KiB and nesting to 64 levels;
oversized events are omitted whole. Bounded linking/deduplication indexes share
the record ceiling. Collection status is reserved outside this budget: at most
16 diagnostic entries, each identity capped at 1024 bytes, plus reason counts.
The byte total excludes JSON envelope separators and collection bookkeeping.
Limits never silently evict earlier events.

Selected artifact references retain their typed serialization. A reference does
not establish that its bytes were retained. Only value refs and stored refs from
jobs executed in the same disposable case can be resolved; external refs are
reported unavailable without fetching. Each diagnostic is capped by
`--artifact-max-bytes` (default 64 KiB), additionally capped at 256 KiB and by the
case observation budget. Secret values use the existing redaction rules,
including synthetic case secrets, in observation keys/values and diagnostic
contents, manifest names and retrieval keys. Every selected binding uses
`artifact-<SHA-256 of original binding name>` as its retrieval filename;
distinct bindings that redact to the same display name retain separate files.
Always retrieve using the manifest's `path`, rather than reconstructing it from
the display name. Literal names never serve as retrieval keys. Occupied keys
are reported as `artifact_collision` omissions instead of replacing bytes;
collection becomes incomplete. Capture errors mark collection incomplete.

A retained manifest's `path` is relative to the case report directory. For the
example, read:

```sh
case_dir=/tmp/observation-report/cases/bounded-observation
artifact_path=$(jq -r '.run.runtime.observation_artifacts[] | select(.status == "retained") | .path' "$case_dir/result.json" | head -n 1)
cat "$case_dir/$artifact_path"
```

Selected diagnostics are included in ordinary inline report artifacts even when
`--artifact-mode` is omitted. They remain available after disposable runtime
cleanup, including empty diagnostics and expected root failure. Unavailable or
omitted refs do not receive a retained retrieval path. Directory suite reports
retain the existing `suites/<relative-suite-path>/cases/...` nesting.

## Prepared completion controls

Use `runtime.backend: sqlite` for a disposable persisted native job. The default
backend remains `memory`. The case closes its database after execution cleanup
and removes its temporary files; the report is the retained evidence.

Prepared completion controls are additive to the runtime case interface:

```yaml
runtime:
  backend: sqlite
  controls:
    max_evidence_bytes: 262144
    barriers:
      - id: answer
        match:
          selector: ./echo
          node_path: example/answer
          cell: root
          occurrence: 1
          attempt: 1
        stdout: '{"output":{"answer":"ready"}}'
    actions:
      - {on: answer.entered, do: release}
```

The recipe must contain that local selector, and its package must exist in the
fixture cell. Controls do not supply missing source or bypass package/tool
preparation. The barrier runs after input validation, defaults, materialization
and object hydration. Scripted stdout traverses normal envelope decoding,
output validation, object publication and artifact processing. `error` scripts
an ordinary application error instead of stdout. A whole-operation mock cannot
also match the selected invocation.

All matcher fields are required and exact. Occurrence counts distinct native
invocation IDs for the selected selector/node/cell within the case; attempt is
the native one-based task attempt. Report counters are not attempt identities.
Missing matches, duplicate selections, unexpected selected retries and unused
controls fail. A barrier without an action blocks until runtime cancellation or
the case deadline and fails as unreleased; expected-error matching cannot absorb
control failures.

`cancel_job` uses an independent controller context to cancel the entered job.
Controlled cases renew one-second native worker leases so invalidation becomes
observable promptly. The controller records request and acceptance separately
from the runtime cause, completion exit, operation exit and engine drain.
Cancellation passes only with accepted native cancellation, a native lease-loss
or job-cancelled cause, persisted `CANCELLED` status, cancelled completion,
cancellation failure kind and no successful job output. Omit `expect_error` for
this action; a matching error string is insufficient evidence. The selected
operation exits before worker cleanup. `drained` follows joining the engine.

`capture_env` on a barrier names prepared environment values to retain. It is
empty by default, forbids protected `C2J_` values and fails if a requested value
is unavailable. Materialized input and selected environment snapshots use the
existing secret redaction. `max_evidence_bytes` bounds each prepared-entry or
gated-review snapshot (default and maximum 262144 bytes); exceeding the bound
fails rather than reporting empty evidence.

A response may specify `after: answer.exited` (or `answer.entered`). Its genuine
pending review is captured on one controller pass; submission occurs on a later
pass after the named barrier event. This preserves an inspectable interval
before continuation. Reports retain the native request and receipt returned by
the ordinary input API, rather than synthesizing receipt IDs.

The additive `runtime.controls` report has `version: 1`, ordered `events`,
`errors`, `complete`, and a persisted `final` job state. Entry events distinguish
the authored selector, resolved selector, native invocation/attempt/result
ordinal and authored node/cell. `review_pending` includes the native form and
the call snapshot before submission. CEL assertions can access `controls`.
Here `complete` means the configured control obligations were satisfied.

Optional `dispatch: {strict: true, allow: [...]}` records native task dispatch
before the environment/permission guard and native `submit_job`, `create_cell`
and `restart_job` calls before authorization. Allow entries use exact `kind`
(`task` or `native`) and `name`, with optional exact `node_path` and `cell`.
Internal preparation tasks also need explicit allowance. Forbidden attempts are
recorded and rejected before the guarded worker or native callback runs.
Permission-denied attempts remain visible when dispatch policy allows them.
The report includes trusted native job/invocation/attempt/result ordinals,
allowed/denied/failed/finished flags, and `dispatch_complete`. Native review
collection persisted by the workflow may not be a worker dispatch; its stored
receipt appears in successful task snapshots instead. This coverage does not
observe arbitrary HTTP calls or subprocess internals. `max_attempts` defaults to
1024 (maximum 4096); overflow fails the case and records dropped evidence.

Optional `snapshot: {files: [candidate.txt]}` captures baseline effects while
the selected operation is blocked and final effects after execution drain.
It retains successful native task outputs, artifact keys and full bounded
artifact bytes, child job identities, and fixture/selected invocation repository
HEAD/tree/refs/index/diff/status and explicitly selected file bytes. Content has
SHA-256 plus base64 of redacted bytes; redaction is explicit. The default total
snapshot bound is 1 MiB (maximum 16 MiB); `max_artifact_bytes` defaults to 256 KiB
(maximum 4 MiB), also bounding selected files. Missing repository/artifact reads,
unsafe file paths, truncation and serialization overflow make evidence incomplete
and fail the case. An explicitly absent selected file is recorded as absent.
Snapshot-enabled cases retain actual operation worktrees until the final snapshot,
then clean them up. Ordinary execution retains its existing cleanup behavior.

`baselines`, `final_snapshot` and `preserved` are additive report fields.
Cancellation requires identical successful outputs/artifacts, repositories,
children and dispatch counts in complete baseline/final snapshots. Healthy release
can add successful effects, so it does not claim `preserved`. Enable both strict
dispatch and snapshots when proving attempted and stored effect preservation.
The neutral CLI example is
`pkg/recipetest/testdata/native-controls/native-controls.test.json`:

```sh
c2j test validate --file pkg/recipetest/testdata/native-controls/native-controls.test.json
c2j test run --file pkg/recipetest/testdata/native-controls/native-controls.test.json --case-timeout 20s --out-dir /tmp/native-controls-results
```

It covers healthy release, cancellation and ordinary-error catches using a real
local package and genuine native reviews. These neutral cases do not establish actual pinned resolver acceptance.

The shipped suite now also contains real passthrough seed, normalization and
continuation packages. The seed publishes a typed checkpoint archive; the answer
binds that archive into its inbox and hydrates its typed object input. Healthy
release publishes a new checkpoint through ordinary extension processing,
normalizes the answer, reaches a gated review and emits one continuation sentinel.
The ordinary-error case uses a real catch and retains the seed checkpoint.

A barrier's optional `capture_prepared: true` retains bounded content/digests for
hydrated top-level object parts and stored bound artifacts, including original
native artifact keys. Optional `probe_tools: [neutral-tool]` runs each exact
executable with `--version` through the prepared tool environment, retains its
bounded redacted stdout, and fails if unavailable. Probes require
`capture_prepared`; they are explicit fixture commands, not passive tracing.
Each evidence item and the whole entry obey `max_evidence_bytes`.

Optional barrier `objects` maps draft names to `{type, metadata, files}`.
`files` maps confined relative part names to inline fixture contents. Only after
healthy release are these parts staged in the actual prepared object outbox and
added to the scripted completion envelope. Stdout references them with ordinary
`{"$object":"draft-name"}` values. Normal extension decoding, schema checks,
archive publication and downstream hydration are retained. Cancellation stages
no completion objects. Escaping or symlinked output paths fail the control.
Do not also define `objects` in stdout when using this field.

For this offline tool fixture, prepend
`pkg/recipetest/testdata/native-controls/fake-bin` to PATH and set
`C2J_TOOL_CACHE_DIR` to a disposable scratch directory before running the shipped
CLI suite. Its synthetic `uv` installer installs only the neutral marker script;
no network package installation or model credential is involved. Normal tool
resolution, setup, activation and invocation still run. The Go
`TestShippedNativeControls` test arranges this environment automatically.

Prepared content capture currently handles top-level object inputs with regular
file parts and stored file bindings. Unsupported directory parts or non-stored
bindings fail capture rather than implying complete evidence. Snapshot archives
retain full bounded checkpoint bytes independently of prepared content capture.

## Ordering real child jobs (unreleased)

These optional controls are unreleased. **0.0.124 does not support them**;
recipes must upgrade to a supporting CLI and worker release before adoption.
The first supporting version is not yet known. To try the checkout examples:

```sh
go build -o /tmp/c2j-native-ordering ./cmd/c2j
/tmp/c2j-native-ordering test run --directory testdata/recipes/native-ordering --case-timeout 10s
```

The examples use disposable local repositories and real embedded jobs, with
fixture reports and synthetic input answers. They need no model or deployment.
`terminal.test.yaml` proves completed and failed children terminate before a
configured report returns. `gated.test.yaml` releases child input at parent entry
and then awaits the real child through normal jobs metadata.

An input response can wait for a root-parent operation to enter:

```yaml
runtime:
  responses:
  - node_path: child/release
    cell: worker
    response: {}
    after_call:
      node_path: gated/release
      cell: root
      occurrence: 1
```

The selector requires a nonempty exact native node path and an explicit positive,
one-based occurrence; `cell` is optional. Distinct invocation identities count
once; retries of the same invocation do not advance the count. Child invocations and mismatched paths or cells
cannot release a gate. Entry is recorded before operation execution, so gating
on entry to a blocking await works; gating on an unreachable later operation
will time out. An earlier entry remains eligible when input arrives later.
Matching deferred responses stay unused while other responses and inputs
progress. An eligible response uses ordinary input validation and attachments;
an attempted validation rejection consumes it. Truly unmatched input still fails.

A submitting fixture can require its exact broker-submitted child to terminate:

```yaml
effects:
  children:
  - recipe: child.yaml
    cell: worker
    await: terminal
```

Only `terminal` is supported when `await` is supplied. Both completed and failed
children satisfy the barrier; the fixture's configured report is preserved.
Submission permissions still apply. Omission keeps asynchronous submission.
Omitting `after_call` keeps immediate matching responses.

Reports expose `runtime.events`; CEL assertions receive that list as `events`.
Positions increase under the runner mutex and describe `operation_entry` for root
operations, `gate_eligible`, `response_release`, `child_submitted`,
`child_terminal` (with observed native status), and `fixture_return`.
Events carry job identities, relevant paths/cells and zero-based response
indices. Release records an ordinary answer attempt, including rejected answers;
eligibility can precede input arrival. The examples assert positions and matching
identities instead of elapsed time.

Case cancellation stops waits and workers before cleanup. Timeout diagnostics
identify unresolved selectors and unfinished child IDs. A timeout cannot satisfy
`expect_error`. Dependencies that cannot become eligible time out; the harness
does not infer general dependency cycles. Unused fixtures still fail cases.

## Scripted responses with native child identities (unreleased)

A native return mock can name its broker-submitted children with `bind`, then
construct a response using `outputs_expr`. These additions require a supporting
CLI and worker; they are not available in 0.0.124. No release tag is assigned by
this example. Build the checkout and run the self-contained suite:

```sh
go build -o /tmp/c2j-native-bindings ./cmd/c2j
/tmp/c2j-native-bindings test run \
  --file pkg/recipetest/testdata/native-bindings/native-bindings.test.json \
  --out-dir /tmp/c2j-native-bindings-results
```

The suite uses embedded SQLite JobDB, disposable Git repositories, one real
successful child and one deliberately failed child. It verifies response IDs
against native submission metadata and terminal observations. The report contains
synthetic fixture data. No server deployment, model credentials or network
service is needed after installing project dependencies.

For example, the submitting mock can declare:

```yaml
match:
  node_path: bindings/submit
  cell: root
behavior:
  mode: return
  effects:
    children:
    - bind: failed
      recipe: bad.yaml
      cell: child
      await: terminal
  outputs_expr: >-
    {"request": {"job_id": children.failed},
     "report": json_encode({"child_id": children.failed})}
```

Matchers keep their existing meanings: `node_path` is an exact native operation
path, `cell` selects the invocation workspace, and `selector` can select an
extension operation. Bindings belong to the entire case, including its child
invocations, and never cross cases. Use distinct names for distinct child effects.
Names become available only after the broker returns a successful submission;
all effects on the current invocation run before its expression. A later mock can
reference the same name. The expression receives only an immutable `children`
map of names to generated job-ID strings, with no inputs, secrets, job searches,
filesystem access or mutable runtime objects.

`outputs_expr` is an explicit CEL expression returning a JSON object. It is not
`${{ ... }}` interpolation. Nested maps and lists remain structured. Use
`json_encode(value)` when the consumer needs a JSON string; quotes and newlines
are escaped using normal JSON serialization. JSON values must use string object
keys and finite numbers; CEL bytes, duration and timestamp values are rejected.
Evaluation has a 100,000 cost limit; encoded reports and complete computed
responses are limited to 65,536 bytes each. Published object-effect references
are merged only if their keys are disjoint from the computed response. A key
collision fails the operation and names the conflicting key.

Existing `outputs` remains literal, including strings resembling expressions.
Do not declare both `outputs` and `outputs_expr`, even with empty or null literal
output. Empty/null expressions, malformed expressions, duplicate binding
declarations and use outside native return mocks fail validation before effects.
Missing or forward names fail evaluation immediately with the missing key and
invocation location. Move the declaration earlier or correct the binding name;
the harness never searches unrelated jobs or waits for a future declaration.
Repeated invocation of a declaration cannot rebind its name: the error names
both invocation locations.

Bound submissions retain case-runtime receipts keyed by parent job, native
invocation ID and child effect index. Task retry and reconstruction within that
runtime restore the original broker ID without submitting again. Scope retries
have distinct invocation IDs and cannot reuse a binding. Receipts survive fixture
object reconstruction, but are removed with the disposable case runtime; they
are not a cross-case checkpoint or a production recovery facility. An interrupted
submission with an unresolved receipt fails closed rather than risking a second
child. It exposes no usable binding; rerun the disposable case to start fresh.

Binding an ID does not wait for the child to finish. `await: terminal` remains
optional, and failed children satisfy that barrier. To observe an active child,
omit `await` and release native input using `after_call` after the relevant parent
call. The binding is already usable while the child awaits input. Permission,
input validation and timeout failures retain their normal behavior. This example
checks c2j's harness contract; Recipes validates its own consumer suite separately.

## Seeded repository references in native mocks

A disposable native case registers portable repository identities before it runs
its root recipe. Native `return` mocks can explicitly use those identities in
proposals, review artifacts, and saved configuration:

```yaml
behavior:
  mode: return
  fixture_refs:
    alpha_repo: {cell: alpha, field: repo}
    beta_repo: {cell: beta, field: repo}
  outputs_fixture_expr: >-
    {"cells": [{"name": "alpha", "repo": alpha_repo},
               {"name": "beta", "repo": beta_repo}]}
  artifacts_expr:
    root-config.yaml: >-
      json_encode({"dependents": [{"repo": alpha_repo}, {"repo": beta_repo}]})
  effects:
    worktree_expr:
      .c2/config.yaml: >-
        json_encode({"dependents": [{"repo": alpha_repo}, {"repo": beta_repo}]})
```

Declare `alpha` and `beta` in `runtime.cells`. References can also select the
implicit primary `root` when `runtime.cell` is omitted. An explicit primary
still must appear in `runtime.cells`. Only seeded cells and `field: repo` are
supported. Each mock sees only its declared aliases, as CEL strings, and pure
CEL built-ins plus `json_encode`; there is no general registry, `cells`, inputs,
secrets, transport path, service, or `children` binding. Aliases must be CEL
identifiers; CEL keywords, `children`, and `json_encode` are reserved. Child invocations reuse
the enclosing case's frozen identities; cases never share them.

Preparation rejects invalid declarations (even unused ones), unsupported modes,
null/empty expressions, undeclared variables, destination conflicts, and count
overflow before starting the root. At the selected mock, all new expressions,
result types, sizes, aggregate costs, object-output collisions and worktree
destinations are checked into detached buffers before any of its artifacts,
objects, writes or child submissions. Destination inspection creates no
folders. Writes recheck confinement and symlinks through the ordinary fixture
writer and retain ordinary snapshots. Artifact text uses ordinary native
publication and review binding.

`outputs_fixture_expr` must produce a JSON object, and conflicts with `outputs`
and `outputs_expr`, even explicit null/empty alternatives. Computed artifact
names must be disjoint from `artifacts` and `effects.artifact_files`. Computed
worktree paths must be disjoint from literal worktree paths, including cleaned
aliases and file/directory conflicts. Absolute paths, escapes and symlinks fail.
Disjoint literal and computed destinations can coexist. Object-effect output
keys must be disjoint from computed output keys.

| Bound per selected mock | Limit |
| --- | ---: |
| Declared repository references | 64 |
| Computed files, artifacts and worktree combined | 64 |
| Each computed text file | 65,536 UTF-8 bytes |
| All computed text files combined | 262,144 UTF-8 bytes |
| Encoded structured output and each `json_encode` result | 65,536 bytes |
| Each expression and all new expressions combined | 100,000 actual CEL cost |

File expressions must return strings. JSON conversion rejects non-string map
keys, non-finite numbers, bytes, timestamps and durations. `json_encode` uses
ordinary JSON escaping for quotes/newlines; JSON configuration text is valid
YAML. Use static file sources for larger unchanged fixtures.

Fixture errors remain failures despite either `expect_error` location or recipe
catches. Diagnostics name the mock, cell, node and failing field/destination.
The guarantee covers seeded computation and destination preflight; later I/O
failures and unrelated child-ID expression failures have no rollback guarantee.
Existing `outputs_expr` still evaluates **after** child effects, retaining its
receipt, retry and merge semantics. It can coexist with computed seeded files,
but one computed output object cannot combine both expression environments.

Existing output objects, artifact strings, fixture source bytes and worktree
strings remain literal, including `{fixture: cells.alpha.repo}` and
`${{ cells.alpha.repo }}`. No automatic interpolation is introduced.

The self-contained example is
`pkg/recipetest/testdata/native-fixture-refs/native-fixture-refs.test.json`.
It runs two SQLite cases concurrently, binds the actual stored configuration to
native review, enters a real empty alpha workspace, submits and awaits a real
alpha child, computes saved configuration, compares the stored review bytes
with saved bytes, and publishes through the real Git operation. Nothing mocks
publication or workspace resolution. Its report records accepted registration
identities in `runtime.repositories.<cell>.repo`, resolved proposals in outputs,
review references in `runtime.reviews`, actual stored review bytes in the
`fixture_refs/compare` call's `artifacts["reviewed.yaml"]`, and published saved
bytes in `runtime.repositories.root.files[".c2/config.yaml"]`. These byte captures
are suitable for direct comparison after the disposable runtime is removed.

This capability was tested on source based on
`7e63a044d3c109eab5403829baf68d370adae98e` plus the seeded-reference change,
with Go 1.26.1 on Linux arm64. Build the supporting source; the pre-change
installed CLI does not support these fields:

```sh
go build -o /tmp/c2j-fixture-refs ./cmd/c2j
/tmp/c2j-fixture-refs test run \
  --file pkg/recipetest/testdata/native-fixture-refs/native-fixture-refs.test.json \
  --parallelism 2 --artifact-mode inline --artifact-max-bytes 524288 \
  --out-dir /tmp/c2j-fixture-refs-results
```

Use the supporting CLI and worker together before adopting this syntax. Recipes
owns migrating its creation fixtures and rerunning its full creation/publication
and timeout checks on that runtime. Root retains creation orchestration. Source
completion does not require a release or deployment, and does not relax
production repository identity, registration, publication or child authorization.

## Native scenarios across jobs and worker sessions

`runtime.controls.scenario` adds deterministic, case-local controls to the SQLite
backend. Existing memory cases, prepared barriers/actions, child bindings and
`responses.after_call` keep their existing behavior. The controller retains the
case deadline, seeded repositories, database, artifacts, consumed responses and
on-disk child receipts while workers are replaced. It never reseeds or resubmits
the root. A failed job does not end a scenario with outstanding obligations.

The runnable neutral examples are in
[`pkg/recipetest/testdata/native-scenarios`](pkg/recipetest/testdata/native-scenarios):

| Suite | Required behavior |
| --- | --- |
| `admission-ordering.test.json` | Two real callers append to one unleased receiver; a third contribution forms a successor blocked through publication. |
| `advancement.test.json` | A real Git landing persists, a separate native job advances the branch, and only then does publish workspace resolution and checkout run. |
| `cancellation.test.json` | Cancel a waiting caller while its receiver finishes, or cancel the receiver before publish dispatch while peers observe its settlement. |
| `worker-recovery.test.json` | Fail after real squash/merge, stop and join, reopen SQLite twice, retry a stable restart request, and use a fresh worker. A newer generation completes while the older continuation is held. Copied prefix tasks do not dispatch again. |

Build the candidate CLI and use it for both validation and execution:

```sh
go build -o /tmp/c2j-native-candidate ./cmd/c2j
/tmp/c2j-native-candidate test validate --file pkg/recipetest/testdata/native-scenarios/worker-recovery.test.json
/tmp/c2j-native-candidate test run --file pkg/recipetest/testdata/native-scenarios/worker-recovery.test.json --parallelism 1 --case-timeout 90s --out-dir /tmp/native-recovery
```

The envelope rejects duplicate names/members, unknown members, explicit nulls, invalid selectors,
undeclared references and declaration cycles. It cannot combine with either
`expect_error`: required outcomes express expected application failures;
controller errors, missing evidence and timeouts always fail acceptance.

| Scenario field | Contract |
| --- | --- |
| `jobs` | Map aliases to exactly one of `root: true`, `child_binding`, or `submission: {caller, task, index, output_pointer?}`. Submission indices are one-based returns from an exact native invocation, never arbitrary job IDs. An optional JSON pointer addresses the persisted task output (for example `/payload/output/items/0`) and must resolve to the same complete native reference. |
| `admission_holds` | `{id, cell, recipe, route, jobs}`. Cell/repository identities come from the seeded registry. `route` uses native `jobType` and optional `taskType`. Matches stay unleased before aliases bind. Release requires every alias to match the native repository, recipe metadata and current route; missing or mismatched targets fail. Release retires the provisional hold for later generations. |
| `dispatch_holds` | `{id, job, task, disposition?}`. Hold the actual scheduled task before its environment/permission guard and workspace preparation. Default disposition requires release. `cancelled` requires accepted native cancellation, settled cancellation, native lease invalidation and worker drain. |
| `events` | `{id, kind, job?, hold?, task?, status?}`. Kinds are `submission`, `dispatch` (arrival at a named hold), `task_result`, and `settled`. Task results require `SUCCEEDED` or `FAILED`; settlement requires `completed`, `failed` or `cancelled`. Results come from persisted history. |
| `actions` | `{id, after_all, do, target?, restart?}`. Prerequisites name events or completed actions. Actions fire once after every prerequisite. Supported operations: `release`, `cancel_job`, `stop_worker`, `reopen_runtime`, `start_worker`, `restart_job`. Cancellation target is independent of its trigger. Lifecycle actions have no target. |
| `outcomes` | Required terminal state for every alias, including continuations: `{status, failure_kind?, message?}`. `message` is a required substring of the native failure. Aliases for the same native job must agree. |
| `retain` | `{id, job, task, part?, artifact?, expect_base64?, compare_to?}`. Select native `input` or default `output`; optionally select one artifact by its exact name. Read complete bytes within bounds, retain native key/size and raw SHA-256, and compare required bytes. Selections already available before reopen are captured and checked again afterward. Missing or changed content fails. |
| `limits` | Positive `jobs`, `events`, `receipts`, `actions`, `report_bytes` limits. Defaults: 128, 1024, 256, 128, 1048576. Maximums: 1024, 4096, 4096, 1024, 8388608. Overflow fails required checks. Dispatch/snapshot/artifact limits remain independently effective. |

A task selector contains exact `task_type`, positive native `attempt`, and at
least one of `node_path`, `invocation_id` or `scope_id`. Optional positive
`occurrence` selects a repeated match; omission requires uniqueness. There are
no wildcards or display-name searches. Workspace scopes are associated with
trusted authored paths at the compiler callsite before scheduling resolution;
fixtures do not compute their hashes. A gate that binds after its task passed
fails rather than claiming a retrospective hold. Replay does not create a fresh
dispatch or submission receipt.

`restart_job` requires a settled named source and:

```yaml
restart:
  path: ['$job', 'sequence:receiver', 'op:publish']
  request_id: native-post-merge
  continuation: continuation
  required_prefix:
    - {task_type: 'squash:squash', node_path: receiver/squash, attempt: 1, occurrence: 1}
    - {task_type: 'merge:merge', node_path: receiver/land, attempt: 1, occurrence: 1}
```

`path` is the exact native scope-site chain, including `$job`, rather than the
story UI's authored path labels. It is decoded from canonical recorded native
identities (JSON string sites and positive occurrence/attempt coordinates).
This avoids relying on story labels that can be absent for failed workspace
scopes. Optional `occurrence` resolves repeated paths. The controller reconciles
one available scope through existing restart discovery, inspects its exact
context, and checks every successful prefix result precedes it. It submits with
no patch, `goal: continue` and the stable request ID. Reusing that ID for changed
selection fails. A continuation is a distinct job outside its source's append
position; the source stays failed. This fixture contract adds no restart grant
to workers and no production API.

Stop closes admission, cancels only worker execution and joins the engine's
lease runners and keepalives. Reopen requires stopped workers, closes storage,
opens the same paths and reconstructs runtime-bound readers. Start constructs
fresh workers, dependencies and input services. Duplicate/incompatible
transitions fail. Parent cancellation and the original case deadline remain
effective across every session.

Reports retain the legacy root `controls.final` and add versioned
`controls.scenario` jobs, native submission receipts, session transitions,
admission receipts, causal event/action receipts, exact retained content and
restart/context/request fingerprints. Native job/goal/result identities remain
separate. Event positions describe observations and enforced prerequisites;
they do not infer a global chronology from polling. Dispatch distinguishes
attempted, held, entered, denied, finished and native cancellation. For absence
checks, `controls.dispatch.forbid` accepts exact dispatch matchers and optional
scenario `job` aliases. Binding reconciles earlier attempts, and final checks
repeat that reconciliation after drain. A forbidden attempt fails even if it
preceded alias binding or a permission guard denied entry. Earlier dispatch
evidence keeps its actual entry and denial fields; reconciliation does not
claim to have prevented an effect. An earlier preparation hold is needed to prevent the publish
operation's dispatch itself. Full dispatch coverage is checked after drain.

Synthetic `runtime.secrets` and `runtime.config` survive reopen. They use the
existing policy model; no extra permissions are implied by scenarios. For
example, a fixture may seed fake credentials and a candidate-trusted hook:

```yaml
runtime:
  backend: sqlite
  secrets: {TEST_TOKEN: synthetic-fixture-only}
  config:
    _c2j_hook_policy_tenant:
      version: 1
      hooks:
        .c2/recipes/hooks/publish.yaml:
          perms:
            jobdb:
              secrets: {resolve: [TEST_TOKEN]}
              jobs: {submit: true}
          source_trust:
            jobdb.secrets.resolve:
              TEST_TOKEN: {mode: candidate}
            jobdb.jobs.submit: {mode: candidate}
```

Use the actual hook path. Empty/omitted grants exercise denial. Cell overrides
use `_c2j_hook_policy_cell_<sha256(canonical repository identity)>` and bind
`repository`; callsite ceilings still apply. Exact-byte retention fails when
secret masking would change the selected bytes. Execute the actual hook/helper
to prove its authorization behavior; a return mock is insufficient. See
[local hooks](GUIDE-Local-Recipe-Hooks.md) and
[node permissions](GUIDE-Node-Permissions.md).

These fixtures use disposable repositories and a neutral publication probe.
Recipes owns its declarative publication cases and product assertions. Adoption
requires the supporting CLI/worker build together; it does not require a new
scheduler, storage, merge or permission interface.

## Response-scoped persistence controls

To distinguish an answer that was never stored from one stored despite an API
error, add `submission` to a `runtime.responses` entry. This opt-in feature
requires `backend: sqlite` and a native `controls.scenario`. Existing response
fixtures and production submission APIs retain their behavior.

```yaml
runtime:
  backend: sqlite
  responses:
  - node_path: example/reply
    fields: {context: diagnostic reply}
    submission:
      id: output-failure
      pending:
        job: root
        task:
          task_type: input:collect_user_input
          node_path: example/reply
          attempt: 1
          occurrence: 1
      after_all: [worker-stopped]
      fault: response_output
      expect_error: {kind: input_not_pending}
```

This fragment needs a containing scenario. The complete
[ten-case example](testdata/repros/native-response-persistence/response-persistence.test.yaml)
and its [build and adoption instructions](testdata/repros/native-response-persistence/README.md)
are runnable from the repository root.

| Member | Meaning |
| --- | --- |
| `id` | Unique nonblank fixture label. The generated native submission ID remains the response index string and is recorded separately. |
| `pending` | Named scenario job and exact native task selector. Require `input:collect_user_input`, positive task attempt and positive occurrence. Node, invocation and scope selectors retain native identity; outer node/cell must agree. |
| `after_all` | Scenario event/action prerequisites for the API call. Capture happens before these gates, permitting capture, stop/join, cancel, then attempted reply. Existing `after` and `after_call` gates also apply. |
| `fault` | Optional `claim`, `response_output` or `settlement`. Reject one selected real SQLite write, with no fixture-authored SQL. Omitting an expected error retains unexpected-error failure. |
| `expect_error` | Optional typed `kind`: `storage`, `input_not_pending`, `stale_request` or `validation`; optional nonblank `contains` narrows the actual message. Omission requires success. |

Malformed, null, unknown and duplicate new members fail validation, as do
ambiguous declarations, inconsistent selectors, unknown references and dependency
cycles. Controlled submissions cannot combine with case/runtime `expect_error`.
Cancellation/deadline errors and preparation, control, inspection or recovery
failures cannot be accepted as submission errors. Unexpected errors and unexpected
success fail immediately after preserving available API/state evidence.

Selection uses persisted native task descriptors and wait records, including
unresolved waits without results. It freezes tenant/job, invocation/scope, task
attempt, wait and output ordinals, request ID, form and input hashes. With a
workspace scope, selection uses that context; otherwise it records the native
containing scope. Repeated controller visits or reopen never renumber occurrences
or replace a captured target with another actionable input.

Once captured, that tenant/job/request is reserved for controlled submission,
including while its submission or recovery gates are pending. Legacy responses
at the same node/cell cannot answer it. They remain available for other native
occurrences, including earlier or later inputs in the same job.
Before legacy matching, the controller refreshes named job identities for each
pending input. Roots and provably distinct submitting parents can receive legacy
replies before their controlled descendants are created. Disjointness requires a
selected caller identity, different unique fixture bindings, or incompatible
native creation provenance, including the actual creating operation's selected
fixture effects. An unpublished binding is never assumed distinct merely because
a native receipt is present. Other aliases are not assumed
distinct: they can select the same child. With insufficient provenance, legacy
answering waits for identity resolution. An unresolved identity cannot become
successful acceptance at the case deadline.

Controlled calls require stopped, joined workers. After capture and reply
preparation, the harness installs triggers limited to the selected job, captured
scheduler payload, output ordinal and response claim lease. A driver counter
records an actual trigger hit independently of public API error translation.
Triggers are removed and their removal verified immediately after the one API
call, including mismatch/failure paths, before recovery can start. Unused faults,
unsupported storage boundaries and incomplete evidence fail the case.

Use response events to gate scenario actions and later replies:

```yaml
events:
- {id: captured, kind: response, response: output-failure, phase: pending}
- {id: attempted, kind: response, response: output-failure, phase: attempted}
- {id: restored, kind: response, response: output-failure, phase: recovered, status: pending}
actions:
- {id: worker-stopped, do: stop_worker, after_all: [captured]}
- {id: expire, do: expire_response_lease, target: output-failure, after_all: [attempted]}
- {id: reopen, do: reopen_runtime, after_all: [expire]}
- {id: resume, do: start_worker, after_all: [reopen]}
```

`pending` means captured native identity; `attempted` requires complete API/state
inspection and removed faults. `recovered` requires `status: pending | completed`.
An optional response-event `after_all` permits a distinct checkpoint after an
explicit reopen; contradictory job/task/hold fields are rejected. These event
gates use the same reference and cycle validation as actions and submissions.

A claim rejection can publish recovered-pending directly with workers joined,
only after proving an actual rejected claim, unchanged actionable input/form,
absent output/receipts/lease and zero subsequent task dispatches. It records
`recovery_mode: not_required`, with no worker replay. An output-write failure
requires actual later-session native job replay and suspension, recording
`recovery_mode: worker_replay` and replay evidence. It cannot use the claim
exception. Checkpoints are snapshots captured before their event is published.
Both branches require a separately declared healthy reply to complete normally.

`expire_response_lease` is fixture-only: it requires joined workers, a reached and
removed recovery fault, and the exact recorded recovery lease ID, owner and
expiry. It updates only that disposable lease's expiration, never output, routing
or settlement. Missing/changed leases fail. Settlement recovery uses the stored
answer without a second submission; recovered-completed preserves its receipt.
Explicit native cancellation needs no lease manipulation or recovery event.
Scenario completion waits for all controlled attempts even if a job is already
terminal. Nothing cancels a job automatically to satisfy an outcome or catch.

`runtime.submissions` (also CEL `submissions`) contains version-1 attempt records:

- `id`, `response_index`, `submission_id`, `pending` and `actor` keep fixture and
  native identities separate. `pending.form` is the frozen form; hashes retain
  exact form/input provenance.
- `attempted`, `api_error`, `error_kind`, `expected_error`, `matched` and explicit
  nullable `returned_receipt` describe only the API call.
- `durable.status` is `present`, `absent`, `unreadable` or `incomplete`, with an
  independently read receipt and exact output ordinal. The reader selects only
  the native result ID and its output envelope, verifying request/submission IDs;
  receipt-shaped attempted inputs never establish acceptance. Replay may occupy
  an empty output slot with an activation; it is not an accepted response.
- `before`, `after` and `checkpoints[].state` separate native status, waiting
  coordinates/handle, actionable discovery, lease, durable output, retained input
  equality, session/position and task dispatches since capture. Original and
  recovery diagnostics remain in the frozen form and immutable native wait input.
- `fault` records trigger/predicate, arm/hit/removal and captured claim lease.
  Checkpoints additionally record recovery mode and native replay identity.
  `controls.scenario.replays` observes actual job-worker entry/return, preserving
  native task-result observers. No collection task worker is fabricated.
- `complete`, `errors` and explicit `omitted` diagnostics describe evidence
  completeness. The existing control and scenario bounds apply. Exact comparisons
  prevented by secret redaction or overflow fail; reported omissions never imply
  absent durable data. Successful API calls alone enter legacy `reviews`.

Matching a submission error only permits further observation. Terminal outcomes,
continuation counts, preserved causes and required checkpoints remain independent
acceptance obligations. The example intentionally keeps approved RF-40
terminal-response-persistence acceptance **FAILED**: harness support does not
resolve unavailable persistence into an honest terminal production result.
