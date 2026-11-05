# Requesting an execution environment from an extension

See the companion [spec](SPEC-Extension-Execution-Negotiation.md) for the process
contract and [execution tracking](GUIDE-Execution-Tracking.md) for recipe-level
requirements.

An extension can read configuration from its normal JSON input and decide
whether the current executor can do the work. When it cannot, it returns its
requirements and exits 75. c2j yields the job; the dispatcher supplies a suitable
executor and the same operation starts again.

## Read the allocation and request what you need

c2j supplies `C2J_EXECUTION_ALLOCATION` as JSON. For example:

```json
{"schema_version":2,"base":"ubuntu-26.04-arm64","resources":{"memory":"16Gi"},"capabilities":["network"],"packages":["uv:ruff==0.12.0","nix:nixpkgs#git"]}
```

The `packages` array contains the full set successfully prepared for this
invocation: repository, recipe, enclosing/current node, job, and extension
dependency declarations combined. Multiple prepared versions can appear;
existing scope precedence still selects which executable a bare command uses.
It does not list arbitrary software installed on the host. Packages are
**informational only**: `packages` is rejected in an `execution_required` request.
Continue declaring dependencies through recipes or the extension manifest.

Missing allocation facts are unknown. A base requirement of exactly `*` accepts a missing
base; other selectors require a reported value. Matching is case-sensitive and
covers the whole name. Only `*` is special; it matches zero or more characters.
There are no OS aliases: `linux` does not match `ubuntu`.

For a configuration that needs macOS, write this to stdout and exit 75:

```json
{"execution_required":{"base":"macos*"}}
```

You can also request minimum CPU, memory, ephemeral storage, and capabilities:

```json
{
  "execution_required": {
    "base": "macos*",
    "resources": {"cpu":"4","memory":"16Gi","ephemeral-storage":"20Gi"},
    "capabilities": ["network"]
  }
}
```

CPU accepts cores or millicores; memory and storage use c2j's existing SI/IEC
quantities. Omit fields you do not need. c2j validates the request and performs
the authoritative comparison. Requesting a handoff when those requirements are
already satisfied is a **protocol error**, including a request containing only
`base: "*"`. c2j preserves enclosing resource minima and capability needs. Base
selectors must refine or encompass enclosing selectors; conflicting requirements
or job-wide overrides produce an error instead of selecting an insufficient
executor.

## A small extension

Save these files under `ops/configured-build/`. This example uses a configurable
base selector; Python must already be available on the initial executor.

`op.yaml`:

```yaml
name: configured-build
command: [python3, run.py]
input_schema:
  type: object
  properties:
    base:
      type: string
      enum: ['macos*', 'ubuntu*', '*']
  required: [base]
  additionalProperties: false
output_schema:
  type: object
  properties:
    ready: {type: boolean}
  required: [ready]
```

`run.py`:

```python
import json
import os
import re
import sys

inputs = json.load(sys.stdin)
raw = os.environ.get("C2J_EXECUTION_ALLOCATION")
if raw is None:
    sys.exit("This op requires c2j extension execution negotiation support")
allocation = json.loads(raw)
if allocation.get("schema_version") != 2:
    sys.exit("This op requires allocation schema 2")

wanted = inputs["base"]
actual = allocation.get("base")
# Escape everything except '*'; fnmatch would give '?' and '[]' extra meanings.
pattern = re.escape(wanted).replace(r"\*", ".*")
sufficient = wanted == "*" or (
    actual is not None and re.fullmatch(pattern, actual) is not None
)
if not sufficient:
    print(json.dumps({"execution_required": {"base": wanted}}))
    sys.exit(75)

# Perform the real work here, after checking the allocation.
print(json.dumps({"output": {"ready": True}}))
```

Call it from a recipe node:

```yaml
- id: build
  op: ./ops/configured-build
  inputs:
    base: macos*
```

The op can instead compute its selector from richer configuration. The recipe
does not have to repeat the derived requirement in `execution_needs`.

## What happens after the request

c2j publishes the scheduling demand and yields without completing the task.
JobDB handles the continuation: earlier completed steps replay from history, and
this op starts again with the same logical inputs and fresh allocation facts.
The extension checks again and may request another handoff if the executor is
still insufficient. No extra recovery state is introduced. The request does not
change the requirements of later nodes or child jobs.

A valid handoff does not enter recipe `catch` or consume a failure retry. An
already-satisfied or malformed request is a normal operation error, so ordinary
catch/retry policies apply to it.

The dispatcher still owns provisioning. For manual execution, resume the same
job with facts describing an executor you have actually provided:

```sh
c2j run --job-id JOB_ID --jobdb JOBDB_URI --execution-base macos-123-456
```

The flag reports the executor; it does not create or transform one. If the op
also requested resources or capabilities, report those facts using the existing
execution flags. Without a compatible executor, the job remains pending.

Negotiate before writes, API calls, or other side effects: the process will run
again, and external effects cannot be rolled back by c2j. Write logs to stderr;
the exit-75 stdout response must contain only the control JSON, with no ordinary
outputs or artifact references. On success, use the existing output contract and
exit 0. Ordinary errors and timeouts retain their existing failure behavior.
