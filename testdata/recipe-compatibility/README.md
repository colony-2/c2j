# Native recipe compatibility

The `default-recipes` job in `.github/workflows/test.yaml` builds this c2j revision
and runs `c2j test run --directory test-repos/recipes --case-timeout 5m` against
pinned recipes and c2ops checkouts. It uses normal public checkout authentication;
no custom token, fixture server, Python driver, generated suite, or migration
patch is required.

The detached c2ops checkout gets a local `main` branch at its pinned commit.
A process-scoped Git URL rewrite directs extension selectors there, so both
`@main` and explicit commit selectors resolve from the checked-out source.
Production extension tools (including Go for rule_gate) are still needed.

Native directory discovery runs every deterministic suite and reports live
suites as excluded. CI retains `suites.log` and per-case JSON results on failure.
The required `test-summary` job aggregates the matrix and compatibility results;
inspect the failed native case for its cause.

These are coupled commits in two repositories: publish the recipes commit
referenced by the workflow before running hosted CI on the c2j commit. The CI
migration itself is implemented in the workflow; there is no patch to apply.

See [the coverage audit](MIGRATION.md) for replacement tests and verification
limits. Recipe decisions live in recipes declarations; runtime guarantees live
in c2j Go tests.
