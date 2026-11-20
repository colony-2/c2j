# Ubuntu compiler activation check

Run this recipe from the published c2j revision after the worker owner adopts that
repair. It uses the existing `linux*`/`docker` route, declares the approved GCC/Go
pair through ordinary worker setup, and fails on a wrong source, non-Ubuntu host,
wrong Nix version, inaccessible Docker or any failed compiler/CGO/race probe.
There are no optional probes. Each command gets a fresh Go build cache.

The owner must start the worker with an isolated `C2J_TOOL_CACHE_DIR` before
preparation begins. Keep that cache and worker for both command occurrences;
repeat the pair if either changes. Do not delete shared caches. Setting the
variable inside either command is too late to isolate preparation.

Submit with the supported custom-recipe option, `--advanced-recipe-file`, and
`--inputs-json '{"published_source":"<published c2j commit>"}'`. Select the
published repository source. Validate the accompanying fixture before submission.
The fixture mocks commands and tests recipe wiring only.

Command success is part of acceptance. Also inspect both task execution records:

- Match `consumer_versions.c2j` and `consumer_versions["c2j.commit"]`, or equivalent
  embedded build provenance, to the published repair used for worker preparation.
  Printing a repaired binary inside the command does not establish adoption.
- Record image reference/digest, base, platform, task occurrence IDs, Ubuntu/Nix
  versions and the exact selectors. The logs include compiler/Go selection,
  versions, CGO settings and the result of each probe.
- Record setup diagnostics from both occurrences. Require native `prepared`,
  then `reused`, with the same nonempty identity and ready retained roots. Record
  package identities, preparation outcomes and exits too. Missing provenance,
  skipped probes or wrong preparation outcomes mean failed acceptance.

This check does not run Pulse's retained uncached full `make test` against tree
`2570cdb40a0851e00158bece1c715310f5311c23`. That work stays with Pulse. Do not
restart or edit historical jobs or change broker submission behavior here.
