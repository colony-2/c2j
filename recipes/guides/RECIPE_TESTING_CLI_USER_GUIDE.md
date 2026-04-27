# c2 Recipe Testing CLI User Guide

This guide covers how to compile, validate, and execute recipe test suites with `c2 recipe test`.

This is not the default day-to-day authoring loop for recipes in this repo.

Start manual authoring with:

```bash
c2j submit \
  --recipe-file ./recipes/my-recipe.yaml \
  --run \
  --embed
```

Use `c2 recipe test` when you need curated suites, mocks, repeated cases, or artifact assertions that go beyond one interactive validation run.

The CLI orchestrates test suites locally and calls per-case server APIs:

1. `POST /api/projects/{projectId}/recipe-tests/cases/validate`
2. `POST /api/projects/{projectId}/recipe-tests/cases/execute`

The server does not persist suite/run objects. Your local output directory is the durable test record.

## Prerequisites

1. `c2` is configured with API URL and project:
   - flags: `--api-url`, `--project`
   - or env vars: `COLONY2_API_URL`, `COLONY2_PROJECT`
2. You can target a recipe in one of two ways:
   - server reference: `--recipe` with exactly one of `--version` or `--ref`
   - inline recipe file: `--recipe-file`
3. You provide suite input from:
   - `--file <path>` or
   - `--stdin`

## Command map

1. `c2 recipe test compile`
   - Compile suite into canonical IR JSON locally.
2. `c2 recipe test validate`
   - Validate each selected case on the server.
3. `c2 recipe test run`
   - Execute each selected case on the server and write local artifacts.
4. `c2 recipe test case validate`
   - Validate one case by ID.
5. `c2 recipe test case run`
   - Execute one case by ID and write local artifacts.

## Common test flags

These flags exist on all `recipe test` commands (including `case` subcommands).

1. `--recipe <name>`
2. `--version <version>`
3. `--ref <ref>`
4. `--recipe-file <path>`
5. `--file <path>`
6. `--stdin`
7. `--format canonical_yaml|canonical_json|compact_yaml|scenario_md`
8. `--case <id>` repeatable, filters suite cases before execution
9. `--strict` reserved for stricter local compile behavior

Validation rules:

1. `--recipe` and `--recipe-file` are mutually exclusive.
2. One of `--recipe` or `--recipe-file` is required.
3. With `--recipe`, exactly one of `--version` or `--ref` is required.
4. `--version`/`--ref` are invalid with `--recipe-file`.
5. One suite source is required. If both `--stdin` and `--file` are set, `--stdin` takes precedence.

## Suite input formats

Supported formats:

1. `canonical_yaml`
2. `canonical_json`
3. `compact_yaml`
4. `scenario_md`

Format resolution order:

1. explicit `--format`
2. file extension inference:
   - `.json` => `canonical_json`
   - `.yaml`/`.yml` => `canonical_yaml`
   - `.md`/`.markdown` => `scenario_md`
3. fallback => `canonical_yaml`

`scenario_md` behavior:

1. CLI extracts the first fenced code block.
2. The block can be YAML or JSON.
3. If no fenced block exists, compile fails.

## Suite/case capabilities

Minimal suite:

```yaml
cases:
  - id: happy-path
    type: recipe_case
    inputs:
      message: hello
```

Supported case `type` values:

1. `op_case`
2. `recipe_case`
3. `integration_case`

Common case fields:

1. `inputs`
2. `target` (required for `op_case`, including `target.node_path`)
3. `mocks` (`return`, `fail`, `passthrough`, `record_passthrough`, `replay`)
4. `assertions`
5. `evaluations`
6. `options` (including policy constraints)

## Op mock matching semantics

`mocks.ops` matching is strict and single-use for safety.

Matching precedence:

1. `node_path + op`
2. `node_path`
3. `op`
4. declaration order tie-break

Consumption behavior:

1. A selected mock is consumed once per unique invocation (`node_path`, `op`, `invoke_seq`).
2. Repeated invocations of the same node/op require additional mock entries.
3. Multi-step task execution inside one invocation reuses the same selected mock.
4. If only consumed mocks match, execution fails with `mock exhausted for repeated invocation`.

## `compile`

Purpose: local-only compile into canonical IR.

Required flags:

1. target recipe flags (server ref or inline file)
2. suite input flags (`--file` or `--stdin`)
3. `--out <path>`

Example:

```bash
c2 recipe test compile \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/my-suite.yaml \
  --out ./tmp/compiled-suite.json
```

Result:

1. Writes canonical IR JSON to `--out`.
2. Fails before network calls if target/suite flags are invalid.

## `validate`

Purpose: compile locally, then call validate endpoint per selected case.

Command-specific flags:

1. `--parallelism <n>` default `4`
2. `--fail-fast` stop scheduling new cases after first invalid/error result

Example:

```bash
c2 recipe test validate \
  --recipe workflows/build-and-test \
  --ref main \
  --file ./tests/build-suite.yaml \
  --parallelism 4 \
  --case smoke \
  --case no-network
```

Behavior:

1. Prints one line per returned case result: `<case-id> <status> (<duration>ms)`.
2. Returns non-zero if any selected case is invalid or errors.

## `run`

Purpose: compile locally, execute selected cases, then write local run artifacts.

Command-specific flags:

1. `--parallelism <n>` default `4`
2. `--stop-on-failure` stop scheduling new cases after first failed/error/invalid result
3. `--case-timeout <duration>` execution timeout sent to server
4. `--artifact-mode none|inline` default `none`
5. `--artifact-max-bytes <n>` default `65536`
6. `--evaluation-mode enforce|report_only` default `enforce`
7. `--out-dir <dir>` default `.c2/test-results/<utc-timestamp>`
8. `--jsonl-events <path>` optional machine-readable events
9. hidden placeholders: `--judge-timeout`, `--judge-max-tokens` (currently unused)

Example:

```bash
c2 recipe test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/my-suite.yaml \
  --parallelism 4 \
  --artifact-mode inline \
  --artifact-max-bytes 65536 \
  --evaluation-mode report_only \
  --out-dir ./.c2/test-results/manual-run \
  --jsonl-events ./.c2/test-results/manual-run/events.jsonl
```

Behavior:

1. Writes per-case status lines to stdout.
2. Returns non-zero if any selected case fails or errors.
3. Stores local run summary and per-case outputs/artifacts.

## `case validate`

Purpose: validate exactly one case from a suite.

Command-specific flags:

1. `--case-id <id>` required
2. `--parallelism <n>` default `1`

Example:

```bash
c2 recipe test case validate \
  --case-id happy-path \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/my-suite.yaml
```

## `case run`

Purpose: execute exactly one case from a suite.

Command-specific flags:

1. `--case-id <id>` required
2. `--case-timeout <duration>`
3. `--artifact-mode none|inline`
4. `--artifact-max-bytes <n>`
5. `--evaluation-mode enforce|report_only`
6. `--out-dir <dir>`

Example:

```bash
c2 recipe test case run \
  --case-id happy-path \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/my-suite.yaml \
  --artifact-mode inline \
  --out-dir ./.c2/test-results/one-case
```

## Local output artifacts

`run` and `case run` write:

1. `summary.json`
2. `summary.md`
3. `cases/<case-id>/result.json`
4. `cases/<case-id>/evaluations.json` (when evaluations are present)
5. `cases/<case-id>/artifacts/*` (when inline artifacts are returned)

When `--jsonl-events` is set on `run`, the file contains:

1. `case_completed` events
2. one `summary` event

## Example suite with mocks, op targeting, assertions, evaluations

```yaml
cases:
  - id: only-generate-node
    type: op_case
    target:
      node_path: root.generate
    mocks:
      ops:
        - match:
            node_path: root.generate
          behavior:
            mode: return
            outputs:
              plan: "build and test"
            artifacts:
              notes/plan.txt: "build and test"
    assertions:
      - type: output_equals
        path: plan
        value: build and test
      - type: artifact_exists
        path: notes/plan.txt
      - type: node_executed
        node_path: root.generate
    evaluations:
      - id: plan-content-policy
        type: text_pattern
        mode: enforce
        source:
          kind: artifact
          path: notes/plan.txt
        config:
          forbid_regex:
            - "(?i)password"
            - "(?i)api[_-]?key"
    options:
      policy:
        required_dependencies:
          - database
```

Run this suite:

```bash
c2 recipe test run \
  --recipe-file ./recipes/my-recipe.yaml \
  --file ./tests/op-case-suite.yaml \
  --artifact-mode inline \
  --out-dir ./.c2/test-results/op-case
```

## Troubleshooting

1. Error: `one of --recipe or --recipe-file is required`
   - Fix: provide exactly one recipe target mode.
2. Error: `exactly one of --version or --ref is required with --recipe`
   - Fix: pick one selector only.
3. Error: `one of --file or --stdin is required`
   - Fix: set one suite source flag.
4. Error: `unsupported format "..."` or parse errors
   - Fix: set `--format` explicitly and verify the suite file structure.
5. Error: `scenario markdown must include a fenced yaml/json block`
   - Fix: add a fenced block with the suite payload.
6. Error: `no cases selected`
   - Fix: verify case IDs and `--case`/`--case-id` filters.
7. HTTP errors from validate/execute
   - Fix: verify `--project`, API URL, auth token, and recipe reference existence.
8. Expected artifacts missing locally
   - Fix: run with `--artifact-mode inline`; `none` omits artifact payloads.
9. Error: `mock exhausted for repeated invocation`
   - Fix: add additional `mocks.ops` entries for each expected repeat invocation of that node/op.

## Related docs

1. `recipes/guides/C2_USER_GUIDE.md`
2. `cli/RECIPE_TESTING_CLI_SPEC.md`
3. `server/RECIPE_TESTING_SERVER_DETAILED_SPEC.md`
