# Native recipe compatibility

The `default-recipes` job in `.github/workflows/test.yaml` builds this c2j revision
and runs `c2j test run --directory test-repos/recipes --case-timeout 5m` against
the current default branch of `colony-2/recipes`. It uses normal public checkout
authentication; no custom token, fixture server, Python driver, generated suite,
or migration patch is required.

Ops resolve normally from the selectors declared by the recipes: explicit
commits retain their versions, while branch selectors such as `@main` resolve
from the upstream repository. CI does not check out c2ops separately or override
Git resolution. Production extension tools (including Go for rule_gate) are
still needed.

Native directory discovery runs every deterministic suite and reports live
suites as excluded. CI retains `suites.log` and per-case JSON results on failure.
The required `test-summary` job aggregates the matrix and compatibility results;
inspect the failed native case for its cause.

The recipes checkout is intentionally unpinned so the required check detects
compatibility with current recipes. New recipes or changes to floating op refs
can therefore affect a rerun of the same c2j commit. The selected recipes SHA is
printed and saved as `recipes-commit.txt` alongside failure diagnostics. Check out that
revision to reproduce the recipe inputs to CI. An exact reproduction also needs
the original op commits if any floating selectors have changed since the run.

See [the coverage audit](MIGRATION.md) for replacement tests and verification
limits. Recipe decisions live in recipes declarations; runtime guarantees live
in c2j Go tests.
