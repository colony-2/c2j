# Historical Backstop regressions for shared upstream ancestry

A recovery agent can reconcile a candidate onto upstream while c2j still records an older managed base. Replaying every commit after that base then replays upstream changes as local work. Later upstream edits can make that replay conflict even though the candidate already contains the upstream history.

The human requested this validation in submission `dbbe92c4-6f83-4887-8a1c-0a26a6376f44`, November 17, 2026 at `06:23:08.841249Z`:

> Please find past jobs where this was a problem and backstop had to step in, create reproduction cases with similar properties, confirm fail with old op and work with updated one

## Incidents used

I read each original native Git diagnostic and the corresponding Backstop investigation. These are reduced reproductions with similar history and file conflicts, not restored application executions or migrations of those jobs. Historical application hashes below are reported identifiers only; the fixtures generate and validate their own objects and never execute against those historical targets.

| Historical job | Original native failure | Backstop finding | Fixture |
| --- | --- | --- | --- |
| Cortex `3M7OO6GyLMkFPvhI8JNaZ4STGPe` | November 15 at `20:12:58.991498967Z`: replay dropped cleanup `67ebcb6`, then conflicted replaying shared review-interface commit `59d0a8d` in `InputDetailPage.tsx` and `ReviewDocuments.tsx`. | Investigation at `20:13:03.339154427Z`: managed base remained `7af6841d`; candidate `ba130e1` already had main `2e1198a0` as parent. Three native replay failures led to Backstop investigations. | `cortex-review-history`: a published cleanup, repeated upstream review edits, then one independent local feature commit. |
| c2j `Wk8CYgJjhGya9Q8hRfMGC9znqgc` | November 16 at `23:57:55.257695089Z`: first of three replayed commits, `8551f48`, conflicted in `go.mod` and `go.sum`. | Investigation at `23:58:00.365737008Z`: local rebase left managed base `a386cc5`, while candidate `02f50e9` already incorporated newer main `a83cca8`; replay tried older JobDB v0.0.86 changes against newer v0.0.88 upstream. | `c2j-dependency-upgrade`: dependency/checksum files change twice upstream, followed by an independent download-limit repair. Dependency text is fixture content, not a real module upgrade. |
| Recipes `3M797193dahkOPVxg2yDd52q0b9` | November 15 at `20:55:58.63045705Z`: first of six replayed commits, `ededfb3`, conflicted in `packages.test.yaml`, `statements.md` and `tools.test.yaml`. | Later Backstop investigation at November 16 `05:05:19.97646221Z`: locally rebased Manager candidate retained old managed base `a61d3ca`; owner diagnosis of the native range was required before validation could resume. | `recipes-manager-history`: three upstream validator files change twice, followed by two local Manager files/commits. |

The original artifact for each is `_c2j/rebase/git-output.txt` on the failed `rebase:rebase` occurrence. Backstop observations are exact `/response` reads on the identified `extension_execution:extension_execution` occurrences under `adaptive3-backstop`. Use `c2j job log JOB --json`, following `next_cursor`, to locate those occurrences; pass the returned exact task ID to `c2j job artifact` or `c2j job log --field /response`. Node paths and timestamps are source locators, not task IDs.

## Reproduction and assertions

`pkg/git/thinpackrebase/historical_backstop_test.go` runs each incident shape twice:

1. The pinned target is already incorporated in the candidate. The exact candidate tree must survive.
2. The pinned target advances beyond the incorporated revision. Only the one or two local commits may replay; an independently constructed target-plus-local-files tree must match exactly.

Each fixture establishes that the recorded base predates shared ancestry, that `merge-base(candidate, target)` is the incorporated revision, and that `target..candidate` contains only the expected local commits. It invokes ordinary native replay without `resolution`. Assertions cover base/candidate outputs, original `rebased_from`, exact tree, local commit count, target ancestry and clean integration state. A failing old operation must restore the original HEAD/tree and clean state; its acceptance still fails.

## Old operation versus corrected operation

The comparison used direct commit objects reconfirmed locally:

- Old operation: `851917122cf794da0dbedcdc2f1367afe98564fa`.
- Corrected operation: `e2cfac04ee9156e349a1f746504dce32a6f2e8ed`.
- Recovered test: saved candidate `7884e2540c3d6a1bf0018c098e4c8b1ce0e07964`, containing only this additional test over the corrected operation.

I exported old source with `git archive` into isolated scratch and copied the identical regression file into both trees. In each tree I ran:

```sh
go test ./pkg/git/thinpackrebase \
  -run '^TestRunThinpackRebase_HistoricalBackstop$' -count=1 -json
```

The old operation exited **1**, failing all six cases on the representative upstream files and restoring clean original candidates. The corrected operation exited **0**, passing all six cases and every exact-tree assertion. The correction excludes commits reachable from the pinned target instead of replaying the entire recorded-base range. Existing worker integration tests separately cover both selectors, native snapshot persistence, worker loss, fresh restoration and completed offline replay. These reduced incidents do not claim recovery, publication or deployment of the three historical jobs.

| Reduced case | Old operation | Corrected operation |
| --- | --- | --- |
| `cortex-review-history/incorporated-target` | Failed (0.11s) | Passed (0.09s) |
| `cortex-review-history/later-target` | Failed (0.13s) | Passed (0.11s) |
| `c2j-dependency-upgrade/incorporated-target` | Failed (0.10s) | Passed (0.09s) |
| `c2j-dependency-upgrade/later-target` | Failed (0.11s) | Passed (0.10s) |
| `recipes-manager-history/incorporated-target` | Failed (0.10s) | Passed (0.08s) |
| `recipes-manager-history/later-target` | Failed (0.12s) | Passed (0.11s) |
