# Nix native-library activation prototype

This experiment checks the native-library portion of
[the repository packages design](../../PROPOSAL_REPOSITORY_PACKAGES.md).
It exercises real Nix package realization, activation, C compilation, and
runtime linking in a disposable container. It does not change the c2j runtime.

Run from the repository root with Python 3, Docker, and network access:

```sh
python3 prototypes/nix-native-environment/prototype.py \
  --output-dir /tmp/c2j-native-results
```

The script uses `nixos/nix:2.35.1` and nixpkgs revision
`b6018f87da91d19d0ab4cf979885689b469cdd41`, matching the existing real-Nix
integration test. It creates and removes its own container, and uses `docker cp`
so the Docker daemon need not share the checkout filesystem. Package realization
can download several hundred MB. All subprocesses have a five-minute timeout.

`--container NAME` can reuse an existing disposable Nix container for debugging;
that container is not removed. The experiment runs Nix garbage collection inside
it, so this option is only suitable for a disposable store.

The output directory retains `report.json`, `derivation.json`, `activation.json`,
and a command transcript in `commands.jsonl`. Activation paths belong to the
container's store; the JSON alone is not a portable installation.

## Mechanism

1. Evaluate a generated consumer `mkShell` containing OpenSSL, zlib, and
   `pkg-config`, with the default Nix C toolchain. Inspect its derivation to
   obtain the exact input derivations and selected outputs, including `dev`.
2. Realize only those input outputs with `--max-jobs 0 --builders ''`, retaining
   GC roots. The generated shell itself is never built. Import-from-derivation
   is also disabled during evaluation.
3. Start the realized Bash builder with an explicitly constructed environment
   and source the realized standard environment's `setup` script. Its hooks
   supply compiler, linker, and `pkg-config` settings. Capture exported values
   using `env -0`; do not run generic build phases or a user `shellHook`.
4. Retain a selected environment map, discarding temporary/build-only state.
   Remove the capture directory before using the result.
5. Launch ordinary processes with the saved environment. No Nix command or
   shell activation is needed for subsequent compilation or execution.

Two settings matter during capture: `NIX_NO_SELF_RPATH=1` prevents the temporary
output directory from leaking into linker flags, and `NIX_ENFORCE_PURITY=0`
permits ordinary development builds using workspace paths. The latter controls
the compiler wrapper's path checks; it does not change Nix's source-build policy.

## Checks and findings

The C probe hashes a message with OpenSSL and compresses/decompresses it with
zlib, checking both results. The experiment verifies:

- Binary-cache-only realization even when ambient Nix configuration enables
  builds and specifies a remote builder.
- Header/library discovery through `pkg-config`, followed by compilation and
  execution of the probe.
- Compilation using only `cc probe.c -lcrypto -lz`, proving compiler/linker
  hooks work without explicit include/library flags from `pkg-config`.
- Execution of the compiled binary with an otherwise empty environment and no
  `LD_LIBRARY_PATH`.
- Successful use of zlib's development output despite its lack of a `bin`
  directory.
- Rejection of non-substitutable inputs, both with and without
  `preferLocalBuild=true`, before their marker-producing build script runs.
- Reloading activation from JSON and recompiling without Nix on `PATH` or any
  additional package-manager calls.
- Successful recompilation after garbage collection of unrooted store paths.
- No mutation of the container's ambient package-discovery environment.

The experiment also confirms that directly using `nix print-dev-env --json`
fails under `--max-jobs 0` because its environment-capture output needs building.
Sourcing the already-realized standard environment succeeds without that helper
build. Consequently, this mechanism needs no exception to the package source
build restriction.

See [the recorded result](result.json) for the tested platform and versions.

## Limits before production integration

This establishes feasibility for native Linux C libraries from one consistent
Nixpkgs package set. It does not implement config loading, c2j task lifecycle
integration, package-scope precedence, library-conflict detection, general
readiness recovery, or pnpm/uv changes. The script recognizes the pinned Nix
image's version-4 derivation JSON and rejects structured attributes and dynamic
derivation outputs instead of pretending to support them.

The export filter covers the tested compiler/linker/discovery variables; it is
not a universal contract for arbitrary setup hooks. Production work needs a
versioned activation contract, tests for additional toolchains/setup hooks,
correct composition with op-provided environment variables, and support decisions
for additional platforms and derivation forms. Setup hooks execute package code
during preparation, as in a normal Nix development environment.

This is a native build environment, not automatic ABI repair for arbitrary
prebuilt executables or implicit browser provisioning. The supported script
platforms are `aarch64-linux` and `x86_64-linux`; only platforms present in the
recorded result have actually been exercised.
