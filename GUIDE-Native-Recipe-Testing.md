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
c2j test run --directory recipe-tests --out-dir .c2j/test-results
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

Runtime fixture vocabulary:

- `runtime.cell` selects the root cell (default `root`). `runtime.cells` maps
  names to `files` (path to inline text) and `file_sources` (destination to source
  file). Sources resolve relative to the suite; destinations stay in the fixture.
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

Worker restart, lease fault injection, snapshot encoding and wire protocol tests
belong in c2j's Go regressions, not recipe suite declarations.

Runtime mocks may match `selector` (the authored selector or resolved revision)
plus `cell` and `node_path`. Set `repeat: true` on a mock to reuse it, for example
a real schema gate. Ordered one-shot mocks remain the default; a required mock
that is never used fails the case. Calls record small input artifact contents
in `calls[].artifacts` for feedback/document-routing assertions.

`runtime.command_sandbox: none` explicitly runs passthrough commands on the host,
inside disposable test worktrees. It overrides only the test execution
environment; the authored command and normal snapshots still execute. Omit it
to use the sandbox declared by the recipe. Use this only for trusted recipes.
`cells.<cell>.file_sources` accepts file or directory sources relative to the
suite. Directory trees exclude `.git` and reject symlinks.

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
