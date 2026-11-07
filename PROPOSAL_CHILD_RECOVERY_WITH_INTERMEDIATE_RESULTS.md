# Future recovery using selected intermediate results

Status: c2j inspection, artifact retrieval, and cumulative snapshot export
implemented, 2026-11-06. Recipe-owned recovery/handoff remains deferred.
See [the usage guide](GUIDE-Job-Inspection.md) for the implemented contract and
current visibility limits. Recovery uses this general inspection interface.

The [current recovery plan](PROPOSAL_AGENT_AUTONOMY_AND_PARENT_RECOVERY.md) uses
the available failure information to submit a new job that starts the assignment
again. That path requires no intermediate-result retrieval. This enhancement
would let a parent preserve useful work by selecting outputs from the failed job
and supplying them to an ordinary new submission. Restarting an execution is not
required in either version.

This specification fits the recipes cell's purpose. Recipes owns selection,
handoff instructions, and the recovery flow. c2j owns the inspection APIs, CLI,
task-token access, and supported artifact/change retrieval.

## 1. Intended workflow

1. The parent receives the child's terminal result through exact-job soft waiting.
2. It inspects a compact job log: which tasks ran, what failed, and which output
   fields and artifacts each task produced. Failed jobs remain inspectable even
   when they never exported final recipe outputs.
3. It reads the relevant fields and files. For example, it might select a design
   document, a patch containing unfinished implementation, and the failed check's
   command log. Selection belongs to the agent; the recipe does not need rules
   for every possible failure.
4. It submits a new job with the corrected assignment and only the selected files
   or typed inputs. The request explains what has already been done, what failed,
   and what the new job should accomplish.
5. The new job uses its normal workflow to inspect the supplied work, finish it,
   validate it, and make any required publication decisions. The parent follows
   its new native job ID and consumes its result.

This does not copy the entire outbox, conversation, execution prefix, or session.
The old job remains unchanged. A selected checkpoint may eventually be useful
where a recipe already supports one, but checkpoints are not required for this
feature. A goal relationship can describe the shared outcome without determining
which execution the parent waits for.

## 2. Existing capabilities and the actual gap

Baseline reviewed: c2j main `089061be7a21cba9673eb2aeedf2aca159694588`.
The inspection implementation adds `pkg/jobinspect` and `c2j job` to that baseline.

- `pkg/story/workflow.go` already exposes `GetJobRunStory`, `GetWorkflowOutcome`,
  `GetArtifactByOrdinal`, and `GetWorkflowArtifact`. The story model includes task
  paths, occurrences, attempts, statuses, errors, outputs, and artifact references.
- `recipe.await_result_soft` supplies an exact job's terminal status and available
  final outputs/artifacts. Those final outputs may be absent when execution fails
  before the recipe exports them.
- `c2j submit --artifact name=path` already attaches local files. Native recipe
  submission also accepts stored artifact references. Neither requires inventing
  a new transport format.
- `c2j job` now exposes compact task logs and selective reads. The task helper
  provides JobDB-permitted reads independently of lineage and submission grants;
  it does not expose general chapter-reading or lifecycle authority to the agent.

Build a supported application interface over the existing story/result/artifact
services. Reuse their decoding and native references. This is not a request for
another event store, recipe-owned chapter parser, or lifecycle controller.

## 3. CLI and API shape

CLI, programmatic callers, and the authenticated task helper share the
`pkg/jobinspect.API` request/result model. These commands are implemented:

```sh
c2j job list --json
c2j job show JOB
c2j job story JOB --json

# Compact task history and available outputs; no artifact bodies by default.
c2j job log JOB
c2j job log JOB --json --limit 25 --cursor CURSOR

# One task occurrence, including its output-field and artifact inventory.
c2j job log JOB --task TASK --json
c2j job fields JOB --task TASK --json
c2j job artifacts JOB --task TASK --json

# Read an exact structured output field, using a JSON Pointer.
c2j job log JOB --task TASK --field /response/summary --output summary.json

# Job-level outcome and exported-output inventory, distinct from task outputs.
c2j job result JOB --json
c2j job result JOB --field /assistant_summary --output result.json

# Download one artifact, preserving its bytes.
c2j job artifact JOB --task TASK --name check.log --output ./check.log

# Cumulative Git state plus retained artifacts through the selected task.
c2j job snapshot JOB --task TASK --json
c2j job snapshot JOB --task TASK --dir ./recovered
git -C ./recovered/worktree diff refs/c2j/base HEAD
```

`TASK` is a stable handle returned by the log. Use c2j's existing occurrence
identity; agents should not synthesize keys from node names, retries, or chapter
ordinals. Repeated execution of the same node must remain distinguishable.
Use `--attempt N` to select a retry; the default is the latest attempt.
For artifacts exported at job level, omit `--task`; ambiguous names within a
scope must return a useful error or accept the returned native artifact reference.

An illustrative default view:

```text
Job child-123: failed — cell example
Failure: verification exited with status 1
Final recipe result: not produced

Task    Step             Status      Available outputs
task-a  implement        succeeded   response; implementation.md; changes.patch
task-b  verify           failed      exit_code=1; check.log

Use --task to inspect an occurrence, then read a field or download an artifact.
```

The task inventory should provide:

| Information | Use |
| --- | --- |
| Native occurrence handle, node path/title, task type, cell, attempt | Identify the operation and distinguish repeated executions. |
| Status, available start/finish times, failure code/message | Explain what happened, including setup failures before an operation ran. |
| Output field names, types, sizes where known, bounded previews | Let the agent choose what to read without receiving large bodies. |
| Artifact name, type, size where known, producer, native reference | Locate exact files and pass references through existing submission APIs. |
| Availability and pagination information | Distinguish missing results from partial inventory, withheld data, or an unfinished task. |

Omit inapplicable optional fields. Do not return a large object of empty context
fields. Keep full inputs, rendered variables, resolved recipe YAML, credentials,
and internal session objects out of the default view. Ordinary output fields must
retain their original types; a missing field is different from `false`, `0`, an
empty string, or an explicit `null`.

## 4. Retrieval requirements

**Bounded discovery, exact selected reads.** Paginate task and artifact inventories;
bound previews and report truncation explicitly. A large field must not silently
become a shortened value. Allow exact selected values to be written to a local
file, including structured JSON. Output limits should give the agent a clear next
command rather than dump a chapter-sized body into its context.

**Useful failure coverage.** Include completed and failed task occurrences when
the enclosing job fails or is cancelled. Report preparation failures even if no
application output exists. Support running-job inspection, distinguishing pending
outputs from absent outputs. Stable handles must survive subsequent task updates.
An explicitly addressed hidden job should remain readable when the caller has
access and its data is retained; deleted or expired data needs an honest result.

**No dependency on replaying the workflow.** Reading persisted task outputs must
not execute commands, require a live worker, or resolve a newer recipe revision.
If full story reconstruction is unavailable, expose the stored task information
that can be decoded and identify the incomplete portion.

**Faithful files and diagnostics.** Downloads preserve bytes, filename information,
and provenance. Command diagnostics retain c2j's command, repository-relative
working directory, timestamps, and stdout/stderr representation. Do not replace
the original command log with a generated summary. Default retrieval is individual
files; no archive or extraction step should be required.

**Clear failures.** Distinguish unavailable, not yet produced, expired, and denied
data as far as the access policy allows. Prevent accidental local overwrite and
unsafe filename traversal. A reported artifact reference whose bytes cannot be
retrieved must not appear as a successful download.

## 5. Access from an ordinary agent task

Provide supported read-only access for a task to inspect its current job and the
children it is coordinating, including terminal children. The CLI should use the
task's supplied connection and credentials. Expose the available actions through
`c2j self` and command help so prompts can point agents there.

Do not require a general chapter-reading/admin token, a restart permission, or a
human approval for each result read. Apply existing access controls to unrelated
jobs and restricted artifacts. Reading a log must not restart, mutate, approve,
or adopt the source job's state. c2j owns the exact permission representation;
recipes should consume this capability rather than reproduce authorization logic.

## 6. Supplying selected work to the new job

The first version can download chosen files and use the existing submit flags:

```sh
# The preceding proposed commands saved these selected files locally.
c2j submit --cell example --recipe-file ./repair.yaml \
  --inputs-file ./request.json \
  --artifact design.md=./design.md \
  --artifact error/check.log=./check.log \
  --artifact changes.patch=./changes.patch
```

This illustrates the existing submission mechanism, not a new generic repair
recipe. The chosen recipe must explicitly bind the submitted artifacts from
`context.artifacts` into the task's inbox or use its existing typed document inputs;
an attachment does not automatically appear in every operation's inbox.

Where the caller already has a native stored reference, reuse the existing typed
artifact submission path. A CLI convenience for attaching such references can
avoid a download/upload round trip later, but it is not required for the initial
feature. Do not introduce a separate artifact manifest protocol.

**Unpublished code needs a usable representation.** A commit ID alone cannot
transfer changes that were never pushed. Likewise, `--from-workspace` refers to
the submitting task's workspace, not the failed child's workspace. The artifact
inventory must expose enough information to retrieve a portable change artifact
with its repository/base context, or use a documented supported snapshot export.
A patch is suitable when it faithfully represents the changes; the export must
account for binary files, modes, and deletions when present. Agents should not
need to decode internal thin-pack objects themselves.

`c2j job snapshot JOB --task TASK --dir DIR` implements the supported snapshot
export. It materializes cumulative Git state and retained artifacts in one call;
the common single-workspace checkout is `DIR/worktree`. Multiple workspaces remain
separate. `snapshot.json` records repository/cell/scope, full base and head hashes,
the actual Git bundle format/prerequisites, native artifact references, provenance,
and exported paths. Reserved snapshot artifacts are represented through this
export rather than mixed into ordinary artifact files.

`--boundary before` selects incoming state; `after` is the default. Failed-attempt
snapshots must be selected explicitly and remain labeled as diagnostics. Without
a task selector, export uses the latest successful task state per workspace.
This is cumulative state at a task boundary, not only that task's diff or a
certification of workflow adoption. No-change tasks may reuse prior bundles.
Older floating checkouts without recorded hashes report unavailable.

The export uses existing Git credentials to fetch exact prerequisite commits,
imports and verifies the bundle, and checks out the saved head. It does not
modify an existing checkout or push. `refs/c2j/base` and `refs/c2j/snapshot` make
reviewing and transferring changes possible with ordinary Git commands.

The new job reads the supplied changes, applies or adapts them in its own managed
checkout, and validates the result. Receiving an artifact does not automatically
restore Git state or certify that prior work is correct. If no usable code export
is available, say so and use the current fresh-start path.

Example future handoff prompt:

```text
Complete the original assignment. Relevant work from an earlier failed job is
in inbox: design.md describes the intended behavior, changes.patch contains
unfinished implementation, and error/ contains the failed verification output.
Inspect and reuse the supplied work where useful, address the failure, and run
the required checks. The patch is input to review and apply; it is not already
installed in your checkout. This is a new job with its normal completion process.
```

The exact filenames in this example describe a selected handoff. General shared
prompts should continue directing agents to standard inbox directories, without
generating a list of every diagnostic file.

## 7. Acceptance and adoption

These are behavior statements; the c2j implementation tests the inspection and
export contracts. Recipe recovery adoption is still a separate step.
Paths identify ownership areas in c2j or this repository; new CLI source layout
is for c2j to choose. Each expectation is at most 30 words.

| ID | Importance | Level / dependencies | Files / area | Expectation |
| --- | --- | --- | --- | --- |
| IR-01 | Critical | Integration / failed native job | c2j `pkg/story/` | A failed job without final recipe outputs exposes completed task outputs and available failure diagnostics. |
| IR-02 | High | Integration / repeated tasks and retries | c2j `pkg/story/` | Repeated node executions have stable distinguishable handles, and paginated discovery does not silently omit occurrences. |
| IR-03 | Critical | Integration / large outputs | c2j CLI and story service | Discovery stays bounded; an explicitly selected field can be exported exactly with its original type. |
| IR-04 | Critical | Integration / artifacts and native submission | c2j artifact service; recipes `coordinate.yaml` | Selected artifact bytes reach the new job unchanged; unselected files and session state are not carried forward. |
| IR-05 | Critical | Integration / ordinary task token | c2j task permissions and CLI | A parent task reads its child's results and artifacts without administrative access; unrelated restricted jobs remain inaccessible. |
| IR-06 | High | Integration / preparation failure and unavailable recipe source | c2j `pkg/story/` | Stored failure information remains inspectable without executing tasks or requiring the original worker and recipe source to remain available. |
| IR-07 | Critical | Integration / unpublished changes | c2j change export; recipes `coordinate.yaml` | A new job can recover selected unpublished changes without fetching an unavailable commit or inheriting the parent's unrelated workspace. |
| IR-08 | High | Integration / cancellation and missing artifacts | c2j story and artifact services | Inspection leaves the source job unchanged and reports unavailable bytes accurately; cancellation never initiates recovery by itself. |
| IR-09 | Critical | Integration / cumulative snapshot export | c2j `pkg/jobinspect/` | A single command produces a checkout at the selected task's saved head, with exact prerequisites and separately named artifacts. |
| IR-10 | Critical | Integration / scoped workspaces | c2j worker and inspection service | Workspace scopes and failed-attempt snapshots remain distinguishable; observing a floating checkout does not pin subsequent execution. |

The c2j inspection/access/retrieval contract now has real-job tests. Next update
the recipe's fresh-submission path to accept the selected
intermediate files and add an end-to-end failed-child-to-new-job handoff test.
Keep ordinary submission and exact-job waiting as the control flow. Until that
boundary works, retain the current plan's simple start-again recovery.
