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
