# Verify hook scenarios

Run the native runtime suite from the repository root:

```sh
c2j test validate --file testdata/recipes/verify-hook/verify.test.json
c2j test run --file testdata/recipes/verify-hook/verify.test.json --out-dir /tmp/c2j-verify-tests
GOPRIVATE='github.com/colony-2/*' go test ./pkg/worker/compiler -run '^TestVerifyHook' -count=1 -v
```

The JSON suite is a native recipe suite (JSON is accepted alongside YAML).
Every runtime case uses isolated JobDB/Git fixtures. Task mocks assert exact
commands, limits, routing, explicit failure handling and native artifact maps.
The report-roundtrip case runs the actual reporting shell with bound artifacts;
the rejected-environment case runs the actual gate and reporting operations.
The other commands are mocked: those cases do not certify the repository suite
or the provisioned toolchain.

Missing required input cases require schema rejection. All three independent
checks have separate failure, launch-failure and timeout cases. Each missing
required artifact and diagnostics export failure prevents a pass. Report failure
propagates to the caller. Compiler tests load the production hook and inject
real typed cancellation at each of the five stages, since native YAML error
mocks only support message/code. A report test checks tool/candidate/skip content,
large log input, missing evidence, and shell-safe handling of candidate text.

Full source acceptance additionally requires disposable real-hook execution with
Go 1.26.1, Node 24, private-module read access, npm access and applicable historical
objects. It must cover success, separate untidy-module/test/skill fixtures,
state preservation, full artifact retrieval and optional coverage skips. The
shared wrapper's mutation rejection and from_current source selection are
parent-owned checks. Keep those results distinct from this offline/mock suite.

Run real, opt-in disposable hook fixtures with the provisioned Go 1.26.1 and
Node 24 toolchain:

```sh
go build -o /tmp/c2j-verify-cli ./cmd/c2j
/tmp/c2j-verify-cli test validate --file testdata/recipes/verify-hook/live.test.json --include-live
/tmp/c2j-verify-cli test run --file testdata/recipes/verify-hook/live.test.json --include-live --artifact-max-bytes 262144 --parallelism 2 --case-timeout 8m --out-dir /tmp/c2j-verify-live
GOPRIVATE='github.com/colony-2/*' go run ./testdata/recipes/verify-hook/state-check.go /tmp/c2j-verify-state
```

The live suite uses the actual candidate hook via from_current in disposable
native runtimes. It runs all commands without op mocks and exports all five
native references through another operation's inbox/outbox. Cases cover success,
untidy modules, failing tests, an unreachable npm registry, unavailable history,
a scoped Git protocol-denial policy during private-module download, and a large
log whose end marker is checked after export. It needs npm connectivity and the
pinned toolchain. Fixture Git configuration is local; the denied-access case
sets policy only on that one subprocess and never edits global credentials.

The standalone state helper loads the production recipe through its parser and
runs each exact shell body separately. It checks porcelain status, binary diff
and every candidate file's content hash after every task, starting with both a
dirty tracked file and a real untracked file. Its evidence directory is outside
the disposable candidate. It complements the native artifact/routing suite; it
does not emulate the native scheduler.

Use the source-built CLI for the live evidence suite: older installed CLIs can
run command passthrough while lacking the observation collector used to retain
full logs. The suite requires complete root observation and retains all five
native references before its disposable database is removed. The larger artifact
budget retains the 192 KiB test log, whose bytes exceed ordinary previews.
The Go fixture also checks that CLI tests cannot inherit enclosing job identity
or child-submission credentials; the production test task clears those variables
while retaining its scoped Git credential socket.
