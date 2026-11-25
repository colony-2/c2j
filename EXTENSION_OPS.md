# Extension ops: user and developer guide

An extension op is a process with a manifest describing its inputs, outputs, and
invocation. c2j supports local directories, Git selectors, and prebuilt Nix
packages. Nix packages expose a small manifest before their executable contents
are downloaded; the original application source can remain private.

Extensions can emit optional tagged metric deltas in their output wrapper,
including known usage before an ordinary failure. See [task metrics](GUIDE-Task-Metrics.md#emit-from-an-extension)
for the versioned wire format, limits and failure behavior.

## Temporary resource shortages

When the runner supplies `C2J_RESOURCE_RETRY_CONTROL=1`, an extension can request
restart from its durable input after a temporary process/thread creation failure.
After stopping all local execution and completing cleanup, emit exactly this
bounded JSON object on stdout and exit **1**:

```json
{"control":{"type":"resource_retry","version":1,"reason":"resource_temporarily_unavailable","source":"local_spawn","phase":"launch","replay":"restart_from_input"}}
```

The allowed source/phase pairs are `local_spawn`/`launch`,
`local_spawn`/`preparation`, and `preparation_diagnostic`/`preparation`.
The complete stdout control must fit in 4096 bytes. Additional fields, duplicate
keys, mixed outputs/objects/artifacts, incomplete capture and unknown versions
are rejected. A root `control` in a successful response is an error. Exit 75
continues to mean execution-requirements negotiation.

The extension must establish that the entire invocation can safely restart.
Recognizing EAGAIN alone does not establish that permission: a previously started
action-capable process, including an earlier repair turn, prevents this control.
Known preparation and a rejected first action-process start are eligible. A
launched outer extension can still be doing preparation. Cancellation, timeout,
capture and cleanup failures take precedence. Do not infer safety from a missing
tool event, no Git diff or a session identifier.

The runner advertises the capability only on a lease with confirmed durable
backend support. Manifest, invocation, inherited and tool environment values
cannot enable it. Without negotiation, preserve ordinary failure behavior.
JobDB computes the wait and resumes the unfinished recipe attempt; this control
does not consume recipe retries or enter catches. Its default wait starts at
2 seconds, doubles to a 30-second ceiling, and uses bounded jitter. Existing
deadlines still apply. Deploy backend support and the reader before producers.

## Calling an op

```yaml
id: analyze-example
version: "1.0.0"
input_schema:
  text:
    type: string
    required: true
sequence:
  - id: analyze
    op: nix:github:example/op-packages/<commit>#analyze
    inputs:
      text: "${{ inputs.text }}"
    timeout: 30s
outputs:
  summary: "${{ sequence.analyze.outputs.summary }}"
```

Replace the example repository and commit with a published package definition.
The attribute `analyze` selects the package for the Nix worker's system, such as
`aarch64-linux` or `x86_64-linux`. Metadata resolution and execution currently need
compatible platforms; cross-platform handoff is not implemented. Explicitly
selecting a different system's package fails before installation.

### Following latest

Omit the commit to follow the definition repository's default branch, or name
a branch explicitly:

```yaml
op: "nix:github:example/op-packages#analyze"       # Default branch
op: "nix:github:example/op-packages/main#analyze"  # Explicit branch
```

These are alternative `op:` values; use one in a recipe node. For "latest
published release," maintain a `release` branch and advance it after publishing
the packages for all supported architectures. There is no automatic highest
version or newest-Cachix-upload selection. See [Nix flake reference syntax](https://nix.dev/manual/nix/2.33/command-ref/new-cli/nix3-flake.html).

Each invocation records its resolved manifest and exact package output.
Installation and replay use that identity even if the branch later moves;
a fresh resolution can pick up an update.

Nix normally caches mutable references for one hour. To check for updates on
each fresh resolution, set `tarball-ttl = 0` in the worker's `nix.conf`. c2j uses
Nix's configured policy; this setting does not invalidate recorded job results
or downloaded package outputs. See [Nix cache settings](https://nix.dev/manual/nix/2.33/command-ref/conf-file.html#conf-tarball-ttl).

### Other selectors and local execution

Other selector forms are `./ops/analyze` and
`git+https://github.com/example/ops.git//analyze@<commit>`.
Use the selector directly in `op:`; Nix package preparation is not supported by
manually wrapping it in an `extension_execution` invocation.

Run a recipe locally using your normal configured cell:

```sh
c2j submit --recipe-file ./analyze.yaml \
  --inputs-json '{"text":"Hello"}' --run --embed
```

### Forwarding optional inputs

Use `optional_inputs` on a direct extension call to forward an override only
when a CEL optional contains a value.

```yaml
op: ./compress
inputs:
  file: ${{ inputs.file }}
optional_inputs:
  level: ${{ inputs.?level }}
```

Each entry must be a single CEL expression that returns an optional. A missing
`level` leaves the key out so the extension can apply its default. Present values,
including `0`, `false`, and `null`, are passed through to extension schema
validation. Declare a key in only one of the two input blocks.

## Artifact file inputs

An extension can ask c2j to stage ordinary artifact references as files. Declare
`x-c2j-artifact-file: true` on direct root input properties in both source and
packaged manifests:

```yaml
input_schema:
  type: object
  properties:
    instructions_file:
      type: object
      x-c2j-artifact-file: true
    prompt_prefix_file:
      type: object
      x-c2j-artifact-file: true
```

Recipes pass references directly, without explicit artifact bindings:

```yaml
op: ./ops/analyze
inputs:
  instructions_file: '${{ context.artifacts["instructions.md"] }}'
  prompt_prefix_file: '${{ sequence.prepare.artifacts["prefix.md"] }}'
```

Ordinary `required` and default rules control omission. Present values must be
stored references with the complete `stored.key` (`jobId`, `taskOrdinal`, `name`,
`sizeBytes`), or external references with `name` and `external.url`. Ordinal zero
and unknown size `-1` are valid. External references support absolute local
`file` URLs (empty or `localhost` host, no query or fragment) and HTTP/HTTPS URLs.
HTTP requires a successful 2xx response, verified TLS and HTTP(S)-only redirects.
`expand` must be false or omitted. Raw paths, bare URL strings, caller-built file
descriptors, artifact sets, attached-result markers, checkpoints and inline
value artifacts are invalid in annotated properties. Unknown reference members
and mixed payloads also fail before the consumer starts.

The subprocess receives each annotated property as exactly `{ref, path}`:

```json
{"instructions_file":{"ref":{"kind":"external","name":"instructions.md","external":{"url":"https://example.org/instructions.md"}},"path":"/invocation/artifact-files-RANDOM/000000"}}
```

Read the supplied absolute path during this invocation. c2j copies exact bytes
into private regular files, including empty or binary data; it does not interpret
text, interpolate expressions, decompress HTTP content or normalize newlines.
Local symlinks may resolve to regular files; directories and special files fail.
Stored lookup uses the consuming tenant and the complete producer job/task/name
identity, independently of the outer logical name. c2j validates and stages every
file before starting the consumer.

Only transient subprocess stdin gets descriptors. Recorded inputs, prepared
metadata, compact attachments and automatic snapshots retain references. Secrets,
checkpoints and unrelated inputs retain their existing handling. A consumer that
prints its inputs or returns file contents can still disclose them in its output.

Files are removed on ordinary completion, failure and cooperative cancellation,
including partial staging failure. Every retry stages fresh bytes; completed
replay uses the recorded result without fetching or staging again. External
contents may change between attempts; use stored artifacts for stable bytes.
Abrupt worker or machine death can leave temporary files. Paths cannot be reused
in later invocations.

The annotation accepts only boolean `true`, only on direct root input properties
with `type: object`. Nested, definition, item, branch and output declarations fail
manifest loading. Secret/checkpoint annotations and schema references cannot be
combined on the same file property, including overlapping declarations in enclosing
compositions, matching patterns and applicable additional-property schemas. Schema
references on the enclosing input instance are also unsupported when file inputs
are annotated. Unrelated child properties, unused definitions, defaults and
examples are excluded from handler-conflict discovery.

Upgrade every compiler/preparer and executing worker before enabling this
annotation: older c2j versions silently ignore it. Then update both consumer
manifests, implement the stdin descriptor reader, and rebuild the consumer package.
Installed `share/c2j/op.json` must match `passthru.c2j`. A dependency update in a
consumer's Go module does not upgrade deployed c2j workers.

## Python libraries

An extension `op.yaml` can select a complete Python environment:

```yaml
name: image-probe
python:
  requires-python: ">=3.10"
  dependencies: ["Pillow==12.0.0"]
input_schema: {type: object}
output_schema: {type: object}
command: [python3, '-c', 'import json; from PIL import Image; print(json.dumps({"output": {"size": list(Image.new("RGB", (2, 2)).size)}}))']
```

The extension block replaces inherited Python requirements. Preparation finishes
before the process runs; a failed install prevents execution. `dependencies:` at
the manifest top level still declares prefixed CLI/native packages; the nested
`python.dependencies` list uses standard Python dependency strings. See
[Python environments](EXECUTION_TOOLS.md#python-script-environments) for reuse,
source builds, interpreter selection and repository defaults.

## Manifest and process contract

A Nix package exposes its manifest as `passthru.c2j` and installs the same JSON
document at `$out/share/c2j/op.json`. For example:

```json
{
  "name": "analyze",
  "version": "1.0.0",
  "command": ["bin/analyze"],
  "input_schema": {
    "type": "object",
    "required": ["text"],
    "properties": {"text": {"type": "string"}}
  },
  "output_schema": {
    "type": "object",
    "required": ["summary"],
    "properties": {"summary": {"type": "string"}}
  }
}
```

- Both schemas are required and use JSON Schema. Input properties can have
  `default` values. Recipe inputs use c2j's recipe schema syntax, shown above.
- The process receives the input object as JSON on stdin. Write a JSON object
  such as `{"output":{"summary":"Hello"}}` to stdout. A plain output object
  is also accepted. Send logs to stderr. A nonzero exit status fails the op;
  successful output is checked against `output_schema`.
- `command` is an argv array. For Nix ops, its first element must be a normalized
  package-relative path under `bin/`. Extra elements are literal arguments.
  Nix manifests do not accept `run` or `shell`; package a wrapper script when
  needed. Merely invoking a CLI such as Black does not make it a JSON-speaking
  op: the wrapper must implement this input/output contract.
- The working directory is the package output, which is read-only. Pass workspace
  paths through recipe inputs using `context.environment.op.worktree_path` when
  needed. Do not write into the package. Existing object and artifact conventions
  also apply to packaged ops; see [execution tools](EXECUTION_TOOLS.md) for setup
  and dependency behavior.
- `env` supplies literal process environment values. The optional manifest
  `timeout` bounds the extension process; the recipe node's `timeout` bounds the
  op invocation. Neither process execution nor its timeout begins during setup.

Local/Git ops use `op.yaml` or `op.yml` in their selected directory, with the same
schemas and process protocol. They also support `run`/`shell` and ordinary argv
commands. Nix packages use the fixed `op.json` layout above.

## Packaging a prebuilt binary

Publish a small repository containing `flake.nix`, `flake.lock`, and `op.json`.
It need not contain the original source or the released binaries. This example
packages standalone Linux binaries already produced by your private build:

```nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";

  outputs = { nixpkgs, ... }:
    let
      manifest = builtins.fromJSON (builtins.readFile ./op.json);
      systems = [ "x86_64-linux" "aarch64-linux" ];
      hashes = {
        x86_64-linux = "sha256-REPLACE_WITH_ACTUAL_HASH";
        aarch64-linux = "sha256-REPLACE_WITH_ACTUAL_HASH";
      };
    in {
      packages = nixpkgs.lib.genAttrs systems (system:
        let pkgs = import nixpkgs { inherit system; };
        in {
          analyze = pkgs.stdenvNoCC.mkDerivation {
            pname = "analyze-op";
            version = "1.0.0";
            src = pkgs.fetchurl {
              url = "https://downloads.example.com/analyze-1.0.0-${system}";
              hash = hashes.${system};
            };
            dontUnpack = true;
            installPhase = ''
              install -Dm755 "$src" "$out/bin/analyze"
              install -Dm644 ${./op.json} "$out/share/c2j/op.json"
            '';
            passthru.c2j = manifest;
            meta.mainProgram = "analyze";
          };
        });
    };
}
```

Replace the release URLs and hashes. Commit `flake.lock` so CI and workers use
the same Nix inputs. Dynamically linked binaries need their runtime libraries
packaged too; scripts need packaged interpreters and correctly bound tool paths.

`pkgs.fetchurl` describes a build-time fetch. Reading `passthru.c2j` does not fetch
the binary. c2j forbids import-from-derivation during inspection, so the manifest
must be available from the definition itself. The packaging repository and its
evaluation inputs, including nixpkgs, may still need downloading.

The copy of `op.json` in `installPhase` makes it a build input, so a manifest
change also changes the package identity. c2j rejects an installed manifest that
differs from `passthru.c2j` (JSON formatting and key order can differ).

## Building and publishing

CI may build; execution workers only consume prebuilt outputs. Run these on a
builder for each supported architecture, or use your configured remote builders:

```sh
nix flake lock
nix eval --json .#packages.aarch64-linux.analyze.c2j
nix build .#packages.aarch64-linux.analyze
nix copy --to 's3://your-op-cache' ./result
```

Publish the output and its runtime closure to a Nix binary cache, and configure
workers with that cache's `substituters` URL and trusted public signing key.
Use your existing Nix cache publishing/signing workflow. Access to private
definition repositories and caches uses the worker's Nix/Git configuration.
No changes to `.narinfo` are required.

Even copying a prebuilt binary into a Nix output is a build step: CI must publish
the finished Nix output. Publishing only the binary at its original release URL
is insufficient for our prebuilt-only workers.

### Using Cachix

Cachix can host the finished op package and its runtime closure. In the package
definition checkout, build and publish on each supported architecture's builder
(with the Cachix CLI installed and a write token configured):

```sh
nix build .#analyze
cachix push your-cache ./result
```

Configure Nix in each execution worker/container to use the cache:

```sh
cachix use your-cache
```

This configures the cache URL and trusted signing key. Private caches also need
read credentials configured before `cachix use`. Alternatively, provision Nix's
`extra-substituters` and `extra-trusted-public-keys` directly, with credentials
for private caches. See [Cachix setup and authentication](https://docs.cachix.org/getting-started).

The recipe still references the Nix definition, for example
`op: "nix:github:example/op-packages/release#analyze"`. Nix obtains the matching
output from the configured cache when execution needs it. The definition
repository supplies the manifest through `passthru.c2j`; Cachix supplies the
package contents. If the exact output is neither installed nor available from
a configured cache, setup fails because execution workers cannot build it.

## Additional tools

Declare Nix runtime dependencies in the package definition and bind them in its
executables/wrappers. A Nix op manifest may additionally declare uv/pnpm tools:

```json
"dependencies": ["uv:ruff==0.11.2", "pnpm:typescript@5.8.3"]
```

These are installed only for a live invocation, before its execution budget.
They are not part of the prebuilt package. To make the entire environment
prebuilt, include those tools through Nix as well. Nix-packaged op manifests
reject `nix:` dependencies; local/Git op manifests and recipe/node declarations
continue to support `nix:`, `uv:`, and `pnpm:`.

Qualified calls currently use `uvx --from ruff==0.11.2 ruff ...` and
`pnpm --package=typescript@5.8.3 dlx tsc ...`. c2j binds them to prepared
executables; they cannot install undeclared versions during execution. Plain
executable names use the nearest declaration's version. See
[execution tools](EXECUTION_TOOLS.md) for scope and collision rules.

## Lazy execution, replay, and testing

Only on a task-result miss does c2j evaluate the Nix manifest, exact output store
path and target system, apply defaults, validate inputs and install the output.
Setup uses `--max-jobs 0 --builders ''`, retains a GC root, verifies the packaged
manifest and executable, and prepares dependencies before invoking the op.
Installation uses the resolved identity without re-evaluating a mutable reference.

Cached and unvisited ops cause no lookup. Replay uses recorded outcomes without
Nix, definition repositories or installed packages. A live task prepares any lost
installation again. Preparation and execution currently share the task timeout;
enclosing deadlines still apply. Resolved extension metadata and setup timing,
store path and reuse/failure diagnostics belong to the consuming task outcome.
There are no separate manifest-resolution or setup tasks.

Normal `go test ./...` includes real Nix integration tests in a disposable Docker
container. They verify that metadata inspection does not fetch the binary,
publish a package to a test cache, remove it locally, and execute through c2j
using only the cache. They also cover missing outputs, manifest disagreement,
invalid inputs, unused branches, and replay after removing tools and definitions.
Docker-dependent tests skip when unavailable or with `-short`.

Run just the packaged-op integration test with:

```sh
go test ./pkg/worker/compiler -run '^TestRealNixExtension$' -v
```

## Discard an attempt and wait

A worker advertising `C2J_EXTENSION_WAIT_VERSION=1` accepts a wait control on
**exit 76**. Checked process-tree cleanup currently requires a Unix worker;
other platforms advertise `0` and cannot acknowledge these waits. Write exactly
one JSON object to stdout:

```json
{
  "wait": {
    "version": 1,
    "until": "2026-12-01T09:00:00Z",
    "dependencies": [
      {"job": "BUILD_JOB_ID", "condition": "complete"},
      {"goal": "REPAIR_GOAL_ID", "condition": "success"}
    ]
  }
}
```

Supply a timestamp, at least one dependency, or both. All conditions must become
ready. An exact job accepts `complete` (any terminal outcome) or `success`. A goal
accepts `success` and can obtain its winner through a replacement job; an open
goal can remain pending indefinitely. A failed exact-job success prerequisite or
explicitly failed goal fails the waiting consumer through normal dependency
failure semantics. Target IDs are literal and belong to the consumer's tenant.
Self-waits, missing targets and dependency cycles are rejected.

Use RFC3339 timestamps with an explicit `Z` or numeric offset and up to nine
fractional digits. c2j normalizes them to UTC and accepts the inclusive portable
range `1677-09-21T00:12:43.145224192Z`–`2262-04-11T23:47:16.854775807Z`.
Already-reached timestamps are valid. Relative durations and local times are not.
Identical dependencies collapse in first-occurrence order; conflicting conditions
for one target fail. Nulls, duplicate keys, unknown fields, extra JSON and controls
larger than the process capture limit (4 MiB) fail validation. Do not mix a wait
with output, artifacts, objects, metrics, `execution_required` or saved state.
Scheduling values undergo the ordinary secret-output checks.

A valid wait **throws away the attempt**. c2j publishes none of its workspace
edits or commits, outbox files, objects, stdout/stderr diagnostics, preparation
results or producer/task metrics. It closes invocation resources and checks that
the entire disposable work directory was removed before scheduling the wait.
Cleanup or uncertain handoff failures stop that activation rather than accepting
a successful wait. Earlier completed tasks and their committed work remain.

The operation stays unfinished. After readiness, c2j restores its incoming
workspace and existing inputs into fresh paths and invokes it again from the
beginning. There is no resume input, saved response, continuation file or saved
extension-resolution result. Existing pins keep their normal meaning; mutable
selectors undergo normal resolution again. Only eventual ordinary success
produces the operation output. A valid wait consumes no failure retry or compiler
operation-chain step. Enclosing deadlines continue across waits, recovery and
fresh execution; the manifest timeout still bounds each process launch. A one
second minimum requeue delay prevents immediately-ready controls from spinning.

c2j cannot undo external API writes, submitted jobs or shared-cache effects.
Decide to wait before irreversible actions, or make reruns tolerate repetition
and rediscover external identities through the existing inputs or external
system. Consumer cancellation stops its wait without cancelling its targets.
After an acknowledged handoff, scheduling conditions survive worker replacement;
a crash before publication can cause a fresh run before those conditions were
registered. An acknowledgment lost in transit is recovered from durable scheduler
state rather than by continuing the old activation.

Upgrade every executing and orchestrating worker before emitting waits. Older
workers treat exit 76 as an ordinary failure and may capture edits. The capability
environment variable is controlled by c2j, overriding manifest and invocation
environment settings. Exit 0 retains its business-output meaning (including a
business property named `wait`); exit 75 retains execution negotiation semantics.

## Rerun with JobDB Backoff (EAGAIN)

When `C2J_EXTENSION_EAGAIN_VERSION=1`, an extension can exit 77 with exactly
one JSON control object on stdout. Without that advertised capability, exit 77
remains an ordinary process failure.

```json
{"eagain":{"version":1,"mode":"persist","next_inputs":{"cursor":"page-2"}}}
```

- **Persist** applies the required `next_inputs` object as a top-level input
  patch (`{}` keeps all inputs). The next launch gets the original inbox,
  the ending Git snapshot, and a fresh outbox populated with the previous
  launch's regular files. A `const` operation still discards new Git changes.
- **Abandon** uses `{"eagain":{"version":1,"mode":"abandon"}}`. It reuses that
  launch's original inputs, inbox, and incoming checkout, but starts with an
  empty outbox. It does not undo external effects such as pushes or API calls.

On a continuation, `C2J_EXTENSION_EAGAIN_RESUME` is a JSON object containing
`version`, the previous `mode`, and a one-based `count`. It is absent on the
first launch. The stdin schema does not change; c2j still resolves symbolic
secrets and worker-local paths for each launch. Do not return secret values or
paths from the current worker in your input patch.

JobDB's default durable backoff governs each wake. Enclosing deadlines and
cancellation still apply; only terminal success publishes downstream results.
Use a selector-backed extension with a pinned Git or Nix source and upgrade both
worker roles before using the protocol. Exit 0 with a business output named
`eagain` remains an ordinary success.
