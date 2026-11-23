# Exact-job consumer acceptance on PostgreSQL

This isolated module runs real PostgreSQL 17.5, pgjobdb **v0.0.27**
(`409a5ef1f19323f86dfb8ab98f20904fc7faf5d4`), blobfs storage and JobDB's real
consumer HTTP server. Its explicit module replacement forces the legacy serving
SDK **f885723cc03c**, even though pgjobdb's module graph requests a newer SDK.
The application module and its JobDB pin are unchanged. Fixture middleware grants
test cell/legacy-selection authority; this is not a deployed-server auth test.

Build from the repository root after clearing ambient live-job identity:

```bash
for variable in ${!C2J_CURRENT_@} ${!C2J_CHILD_JOB_@} ${!C2J_WORKSPACE_@}; do
  unset "$variable"
done
unset C2J_JOBDB C2J_TENANT_ID C2J_OBJECT_OUTBOX C2J_TEST_SUBMIT_BINARY C2J_REQUIRE_EXECUTION_BASE_CATALOG
export GOWORK=off
go test -mod=readonly -c -o /tmp/c2j-exact-recipejob.test ./pkg/recipejob
go test -mod=readonly -c -o /tmp/c2j-exact-ops.test ./pkg/ops/recipe
cd testdata/exactjob-postgres
go mod tidy -diff
go build -mod=readonly -o /tmp/c2j-pg-pinned-fixture .
/tmp/c2j-pg-pinned-fixture \
  -recipejob-test /tmp/c2j-exact-recipejob.test \
  -ops-test /tmp/c2j-exact-ops.test
```

PostgreSQL cannot initialize as root. On the Linux root acceptance host, the last
command runs through `setpriv --reuid=65534 --regid=65534 --clear-groups`, with a
writable download cache supplied by `-cache`. Run directly as an ordinary user
elsewhere. Both binaries and fixture must be readable/executable by that user.
No Docker daemon, serving database or worker upgrade is used. The fixture deletes
its disposable database/blob directory on exit; its public download cache remains.

`TestExactJobConsumerReceiptsAndRestart` sends four concurrent copies of one
frozen ordinary descriptor with a reserved destination. It tests both missing and
pre-registered chapter-schema states, compares returned captured identity, checks
changed payload conflict and replays the original restart request ID independently.
The adapter removes only GoalID from real returned handles to exercise the future
goal-free response shape; it does not replace backend writes or identity checks.

`TestExactJobLegacyFrozenRecovery` accepts an original selected operation, loses
its response and recovers through the unchanged format-1 descriptor. It also
accepts a nil-schema shared restart directly through the old runtime and replays
the exact request ID/mode through the current c2j wrapper. Changed mode conflicts;
fresh selected preparation rejects without destination publication.

`TestExactJobBackendIsolation` covers failed/cancelled A under ordinary completion,
soft inspection and success-based result waits. B succeeds in A's legacy goal;
only an explicit B target receives B's output/artifact. It reads exact artifact
bytes, retains the integer 9007199254740993 and compares A's settled binding before
and after B collection. PostgreSQL recovery reconnects the client, **not** the
server; SQLite's corresponding fixture closes/reopens its storage.

For an owner reproduction without any c2j imports, schema wrapper or compiled
test binaries, run the fixture with `-raw-receipts 64`. A corrected 8-round run passed,
but the 64-round run repeatedly failed unchanged replay; this is a race, so a
short green run does not resolve the failure. It sends four identical
ordinary JobDB requests per fresh tenant using a reserved destination and the
default schema, then retries uncertain calls unchanged. Transient uncertainty is
diagnostic; failure to reconcile the unchanged operation fails acceptance.

The harness runs both binaries even if one fails and exits nonzero on any failure.
It does not label a missing PostgreSQL URL as a pass: ordinary package tests run
toy/SQLite/HTTP; only this opt-in harness adds the PostgreSQL cases. Review material
records actual backend outcomes, including the reproduced concurrency failure.
