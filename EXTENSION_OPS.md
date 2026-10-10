# Extension ops: user and developer guide

An extension op is a process with a manifest describing its inputs, outputs, and
invocation. c2j supports local directories, Git selectors, and prebuilt Nix
packages. Nix packages expose a small manifest before their executable contents
are downloaded; the original application source can remain private.

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

Other selector forms are `./ops/analyze` and
`git+https://github.com/example/ops.git//analyze@<commit>`.
Use the selector directly in `op:`; Nix package preparation is not supported by
manually wrapping it in an `extension_execution` invocation.

Run a recipe locally using your normal configured cell:

```sh
c2j submit --recipe-file ./analyze.yaml \
  --inputs-json '{"text":"Hello"}' --run --embed
```

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

When a Nix op is reached, c2j evaluates its manifest, exact output store path,
and target system together. It uses the manifest for defaults and validation.
Only an uncached activity requests installation of the recorded output. Setup
uses `--max-jobs 0 --builders ''`, retains a GC root, verifies the packaged
manifest and executable, and prepares extra dependencies before running the op.
It does not re-evaluate a mutable package reference during installation.

Unvisited ops cause no lookup. Replay uses the recorded manifest and task
results without Nix, definition repositories, or installed packages. If a live
task loses its package, it returns to setup outside the op's timeout. Setup
diagnostics include the output store path, duration, reuse/failure outcome, and
the setup task ordinal. Recipe/sequence/job deadlines still apply.

Normal `go test ./...` includes real Nix integration tests in a disposable Docker
container. They verify that metadata inspection does not fetch the binary,
publish a package to a test cache, remove it locally, and execute through c2j
using only the cache. They also cover missing outputs, manifest disagreement,
invalid inputs, unused branches, setup timing, and replay after removing tools
and definitions. Docker-dependent tests skip when unavailable or with `-short`.

Run just the packaged-op integration test with:

```sh
go test ./pkg/worker/compiler -run '^TestRealNixExtension$' -v
```
