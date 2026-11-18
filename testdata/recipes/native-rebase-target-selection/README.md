# Native rebase target selection

These six suites execute real managed Git operations for both `rebase` and
`thinpackrebase`. Only command operations use passthrough mocks. Every case
checks restoration in a separate following task. Run with a CLI built from
this checkout:

```sh
go build -o /tmp/c2j-replay ./cmd/c2j
for suite in testdata/recipes/native-rebase-target-selection/*.test.yaml; do
  /tmp/c2j-replay test run --file "$suite"
done
```

The fixtures were recovered from Recipes job `3M797193dahkOPVxg2yDd52q0b9`,
its advice task started November 17, 2026 at `00:39:17.956758835Z`, artifacts
`review/checks/managed-rebase-*.yaml`. The two default defect cases now require
successful adoption/integration instead of catching `REBASE_CONFLICT`. They
retain the older recorded base and use only `target_base_hash`. Both selectors
must pass. The ancestor case preserves exact HEAD/tree and the sole temporary
hook commit. The later case requires target ancestry, two pending commits and
feature, hook and upstream contents. Explicit-base, normal and genuine-conflict
controls retain their original meaning.

Fixture scripts write ordinary newline-terminated contents; the recovered JSON
escaped those Python literals twice. Selector branches are explicit native ops,
so an unresolved operation template cannot bypass execution. These recipes mutate
only their disposable fixture repositories and do not run the original hook.

Compiler tests in `thinpackrebase_integration_test.go` additionally interrupt
accepted outcomes, remove checkouts, advance upstream, replay offline and inject
base-setting, bundle/diff generation, upload and pre-outcome failures. They use
SQLite and HTTP-backed runtimes. Scoped and separate-checkout cases check managed
workspace isolation. The explicit-resolution suites retain their four-confirmation
contract and genuine pending-conflict repair route.
