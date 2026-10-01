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
