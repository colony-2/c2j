# Default recipe compatibility

CI runs the complete recipes test suite against the newly built c2j binary.
Recipe and c2ops revisions are pinned in `.github/workflows/test.yaml`.
Both pinned revisions are publicly fetchable; no custom checkout token is needed.

The c2ops checkout is detached because CI pins a commit SHA. Fixture preparation
creates a local `main` branch at that exact commit: the recipes use c2ops `@main`
selectors, and `run-defaults.sh` redirects their Git requests to this checkout.
This keeps selector resolution pinned without fetching the latest upstream main.

The fixture's in-memory HTTP server is built with the same JobDB version as c2j,
read from c2j's module graph during preparation. Its original older dependency
does not support the lease renewal protocol used by the current client. This
updates only the disposable fixture checkout, not the upstream recipes repo.

All five suites must pass. Their combined output is retained as `suites.log`
alongside available worker logs in the `recipe-regression-logs` failure artifact.
The `test-summary` job only aggregates results; investigate the failed step in
`default-recipes` for the underlying error.

The pinned recipes revision does not export earlier verification artifacts in
its final build/evolve job results. `recipes-verification-export.patch` is the
companion production recipe fix: it forwards stored evidence references through
the public wrappers and strengthens the existing full lifecycle assertions.
It changes no model decisions, verification commands, waits, or merge behavior.

CI applies this patch before running all suites. Remove it from CI and advance
the pinned recipes revision once the companion change is merged upstream.

The patch is also ready to apply in a writable recipes checkout:

```sh
git -C /path/to/recipes am /path/to/c2j/testdata/recipe-compatibility/recipes-verification-export.patch
```

The c2j fixes and this recipe change are both required for the complete
consultation → child merge → parent resume/merge lifecycle. No fixture assertion
is disabled to accommodate the older recipes revision.


## Native suite handoff

Companion recipes commit `01054fb0fc5c0ff168d69f102ed9775382381364` removes the external harnesses.
Its 220 deterministic cases passed through native directory discovery; three live
suites are explicitly excluded. It requires c2j `9e34b70` or later.

Publish that recipes commit before updating the required CI checkout. Then apply
`native-ci-after-publication.patch` from this directory: it advances both pins,
runs native suites, and removes legacy Python/server/recipe-patch preparation.
The pinned c2ops local `main` still resolves authored `@main` selectors reproducibly.

```sh
git apply testdata/recipe-compatibility/native-ci-after-publication.patch
```

The patch is deliberately not applied until its recipes SHA is fetchable. The
current workflow continues to use the published legacy pin in the meantime.
