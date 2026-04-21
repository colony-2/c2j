# Extension Ops Guide

## Overview

Extension ops let you package an operation as a directory with an `op.yaml` manifest plus an executable command.

There are two selector forms:

- cell-local extension ops: referenced by local path such as `./tools/ops/echo`
- git-backed extension ops: referenced by git selector such as `git+https://github.com/acme/repo.git//tools/ops/echo@main`

At runtime, an extension op receives a JSON input object on stdin and is expected to write JSON on stdout.

## Choosing A Selector

Use a cell-local path when the op lives alongside the current cell or repository content.

Local selector:

```yaml
sequence:
  - id: say_hi
    op: ./tools/ops/echo
    inputs:
      message: hello
```

Git selector:

```yaml
sequence:
  - id: say_hi
    op: git+https://github.com/acme/repo.git//tools/ops/echo@main
    inputs:
      message: hello
```

Supported selector forms:

- `./path/to/op`
- `../path/to/op`
- `git+<repo-url>//<repo-relative-path>@<ref>`

Resolution notes:

- for local recipes, `./...` and `../...` resolve from the local project/worktree
- for git-backed recipes, same-repo `./...` selectors resolve within the recipe source repo at the recipe's ref
- same-repo selectors must not escape the recipe source repo with `../...`
- this guide documents the selector-based author model rather than the old discovered-op compatibility path

## Manifest

Each extension op directory contains `op.yaml`.

Example:

```yaml
name: echo
description: Echo input back to the caller
version: 1.0.0

shell: bash
run: python3 main.py
timeout: 30s

env:
  PYTHONUNBUFFERED: "1"

input_schema:
  type: object
  required: [message]
  properties:
    message:
      type: string
    ref:
      type: string
      default: "${{ context.git.ref }}"

output_schema:
  type: object
  properties:
    message:
      type: string
```

## Manifest Fields

- `name`: optional display/runtime name.
- `description`: optional description.
- `version`: optional version string.
- `shell`: shell used with `run`. Defaults to `bash` when available, otherwise `sh`.
- `run`: shell command to execute.
- `command`: argv form alternative to `run`.
- `env`: extra environment variables added to the process.
- `timeout`: Go duration string such as `30s` or `5m`.
- `input_schema`: required JSON Schema-like input schema used for validation and defaults.
- `output_schema`: required JSON Schema-like output schema used to validate stdout.

The process working directory is engine-controlled. For selector-resolved extension ops, it is the op directory.

## Inputs

Recipe inputs are passed under the node's `inputs:` block:

```yaml
sequence:
  - id: say_hi
    op: ./tools/ops/echo
    inputs:
      message: "${{ inputs.title }}"
```

The runtime behavior is:

1. Start with the authored `inputs` map.
2. Apply any extension-op schema defaults from `input_schema`.
3. Resolve template and CEL expressions.
4. Validate the resolved payload against `input_schema`.
5. Marshal that payload to JSON and pass it to the extension process on stdin.

### Defaults

Extension ops use the standard schema `default` keyword inside `input_schema`.

Example:

```yaml
input_schema:
  type: object
  required: [message, ref]
  properties:
    message:
      type: string
    ref:
      type: string
      default: "${{ context.git.ref }}"
    config:
      type: object
      properties:
        label:
          type: string
          default: "${{ inputs.title }}"
```

Key points:

- defaults only fill missing fields
- explicit user input wins
- string defaults can use normal `{{ ... }}` and `${{ ... }}`
- defaults can be nested inside objects and arrays
- `required` plus `default` is allowed

## Process Contract

### Stdin

The extension process receives a single JSON object on stdin.

Example stdin:

```json
{"message":"hello","ref":"main"}
```

### Stdout

The process must write JSON to stdout.

The simplest form is a plain output object:

```json
{"message":"hello"}
```

Stdout may also use this envelope:

```json
{
  "output": {
    "message": "hello"
  },
  "artifact_refs": {
    "report": {
      "external": {
        "url": "https://example.com/report.txt",
        "expand": false
      }
    }
  }
}
```

Notes:

- extension ops understand the `output` / `artifact_refs` envelope
- if `output_schema` is present, the final output object is validated against it

## Environment

Extension ops do not receive implicit runtime metadata environment variables.

Current behavior:

- the declared input payload is delivered on stdin as JSON
- host execution does not inherit the ambient parent process environment
- `sandbox.type: shai` follows the same contract
- manifest `env` is the only environment passed to the extension process

So the process boundary is explicit:

- stdin carries the structured input payload
- manifest `env` carries any additional environment the extension author intentionally declared

## Sandbox

Extension ops support a reserved `sandbox` input field that is not passed through to the extension payload.

Example:

```yaml
sequence:
  - id: run_sandboxed
    op: ./tools/ops/echo
    inputs:
      message: hello
      sandbox:
        type: shai
```

Supported values today:

- `type: none`
- `type: shai`

For `shai`, inline config can be merged into the local `.shai/config.yaml`.

## Validation Timing

Extension ops are resolved at execution time.

That means:

- the static recipe schema only knows that `inputs` is an object
- concrete `input_schema` validation happens after the op is resolved
- defaults are still applied before template resolution once the op is resolved

## Example

Directory:

```text
tools/
  ops/
    echo/
      op.yaml
      main.py
```

`op.yaml`:

```yaml
name: echo
shell: bash
run: python3 main.py
input_schema:
  type: object
  required: [message, ref]
  properties:
    message:
      type: string
    ref:
      type: string
      default: "${{ context.git.ref }}"
output_schema:
  type: object
  properties:
    echoed:
      type: string
    ref:
      type: string
```

`main.py`:

```python
import json
import sys

payload = json.load(sys.stdin)
json.dump(
    {
        "echoed": payload["message"],
        "ref": payload["ref"],
    },
    sys.stdout,
)
```

Recipe:

```yaml
id: example
version: "1.0"
input_schema:
  title:
    type: string
    required: true
sequence:
  - id: echo
    op: ./tools/ops/echo
    inputs:
      message: "${{ inputs.title }}"
outputs:
  echoed: "${{ sequence.echo.outputs.echoed }}"
  ref: "${{ sequence.echo.outputs.ref }}"
```

If the recipe is run with:

```yaml
title: Hello World
```

The extension process receives stdin equivalent to:

```json
{"message":"Hello World","ref":"<resolved from context.git.ref>"}
```

## Practical Guidance

- Prefer stdin JSON for all business inputs.
- Use manifest `env` only for explicit process configuration that is not part of the typed input payload.
- Use `input_schema` and `output_schema` whenever possible so validation failures are immediate.
- Use schema `default` for missing-field behavior instead of baking defaults into the script.
- If you need portable, replay-friendly behavior, keep the extension process deterministic with respect to its stdin payload.
- Use cell-local paths for ops that live with the current cell or repo.
- Use git selectors for ops loaded from another repo or pinned ref.
