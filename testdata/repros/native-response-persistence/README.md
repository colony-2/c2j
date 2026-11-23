# Observe native response persistence failures

This suite rejects real SQLite writes during native response submission, then
observes the existing JobDB recovery. It covers form and structured input at all
three boundaries, a healthy reply and explicit cancellation. It runs only against
disposable local databases. The earlier original and recovery failures are real
native task errors doubled by the fixtures; they are neutral diagnostic data.

Build from the delivered c2j source, record the revision and binary identity, and
run from the repository root:

```sh
git rev-parse HEAD
git status --porcelain
go list -m github.com/colony-2/jobdb modernc.org/sqlite
go build -mod=readonly -o /tmp/c2j-response-controls ./cmd/c2j
go version -m /tmp/c2j-response-controls
sha256sum /tmp/c2j-response-controls
/tmp/c2j-response-controls test validate --file testdata/repros/native-response-persistence/response-persistence.test.yaml
/tmp/c2j-response-controls test run --file testdata/repros/native-response-persistence/response-persistence.test.yaml --out-dir /tmp/response-persistence
```

The ten cases must pass. Inspect `summary.json` under the output directory:
`results[].run.runtime.submissions` records each controlled attempt, including
expected errors. `reviews` continues to contain successful API calls only.
The suite independently asserts terminal outcomes, checkpoint evidence and
continuation counts; merely matching an API error cannot pass it.

Run the negative example separately. It **must exit nonzero**, preserving the
unexpected API error and the independently inspected absent durable receipt:

```sh
/tmp/c2j-response-controls test run --file testdata/repros/native-response-persistence/unexpected-error.test.yaml --out-dir /tmp/response-unexpected
```

The resolved dependencies are JobDB
`v0.0.99-0.20261121090504-28ba72a3ea5a` and SQLite driver `v1.50.1`.
This feature retains the upstream dependency versions and production APIs. The SQLite driver is now a
direct harness dependency so its case-local trigger counter can prove a rejected
write reached the chosen boundary, even when API error translation hides SQL.

| Case | API evidence | Immediate durable receipt | Required observation |
| --- | --- | --- | --- |
| Claim | `storage`, no returned receipt | Absent | Identical actionable pending input, zero continuations, no replay; a declared healthy reply then completes. |
| Response output | `input_not_pending`, no returned receipt | Absent | Temporary recovery lease; actual native replay restores pending input before a healthy reply completes. |
| Settlement | `storage`, no returned receipt | Present | Native replay consumes that answer once; reopening preserves its receipt and one continuation. |
| Healthy | Success and returned receipt | Same receipt | Normal completion and one continuation. |
| Cancelled | `storage` on this pin, no returned receipt | Absent | `CANCELLED`, retained waiting handle, no actionable pending input or continuation. |

The cancellation classification is the actual current API result, distinct from
the job's native cancellation state. Older diagnostic results are not an error
mapping contract. A dependency change must be checked against the real writer
and current typed errors.

For Recipes adoption, pin the **published commit containing this example** in
Recipes' existing runtime dependency, then build/use that exact source for these
commands. The candidate review identifies its exact local revision; publication
may rebase or squash it, so use the final publication receipt to obtain the
available commit. Verify `vcs.revision` and `vcs.modified=false` in `go version -m`
against that pin. Older binaries may silently ignore new response members;
checking only `c2j version` is insufficient. An installed worker must establish
its own build identity before worker-based checks; source acceptance alone does
not prove a worker upgrade.

Recipes retains its production integration fixtures, real cause comparisons and
production dispatch prohibitions. Adopt the controls in those existing fixtures;
this neutral suite does not replace them. See
[the response control contract](../../../GUIDE-Native-Recipe-Testing.md#response-scoped-persistence-controls)
for selectors, lifecycle gates and evidence fields.

Approved RF-40 wording remains:

> Cancellation remains cancellation; unavailable native input or persistence terminates honestly with original and recovery causes.

**RF-40 terminal-response-persistence acceptance: FAILED.** These tests demonstrate
pending restoration and durable-answer recovery. They do not demonstrate honest
terminal failure when response persistence is unavailable. That behavior needs
separate resolution. This feature adds no automatic cancellation, synthetic
worker-input failure, production fail-input API or terminalization policy.
The original incident and parent jobs are untouched.
