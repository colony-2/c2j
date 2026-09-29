# Default recipe compatibility

CI runs the complete recipes test suite against the newly built c2j binary.
Recipe and c2ops revisions are pinned in `.github/workflows/test.yaml`.
The recipes fixture commit `84de797` must be pushed to `colony-2/recipes` before
the new CI job can fetch it; it was present only in the local checkout during
this validation.

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
