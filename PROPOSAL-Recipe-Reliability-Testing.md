# Run c2j's CI checks through a local build hook

The repository-local [verify hook](.c2/recipes/hooks/build/verify.yaml) validates
the current candidate using the checks in
[`.github/workflows/test.yaml`](.github/workflows/test.yaml). Each key step is a
separate durable `command_execution` task, rather than a combined script runner.

| Task | Responsibility | Limit |
| --- | --- | --- |
| `check_environment` | Require Bash, Git, Make, Go 1.26.1, Node 24, npm and npx; record versions, candidate and platform. | 2m |
| `check_modules` | Run `make check-mod` without changing module files. | 5m |
| `test_integration` | Run `go test -count=1 -v -tags=integration ./...`. | 30m |
| `check_skills` | Run `npx -y skills add . --list --full-depth`. | 5m |
| `summarize` | Bind prior logs into its inbox and publish the local result and observed skips. | 2m |

All commands run at the candidate repository root, including when a request
names a subdirectory. Request text and target directories never become shell
source. A state-machine environment gate routes invalid environments directly to
reporting, marking validation checks not run. Once the environment passes, each
independent check runs even when an earlier check fails or cannot launch.
Cancellation propagates; it does not become a synthetic verification result.

The hook implements `c2.build-hook/v1`. Its `status` is `passed` only after the
environment and all three checks succeed, reporting succeeds, and all five
required evidence artifacts exist. Nonzero exits, timeouts, missing outputs,
evidence export errors and unsupported API versions cannot pass. Reporting or
infrastructure errors may propagate to the shared wrapper's failure handling.
The `artifact_refs` output contains native references to `environment.log`,
`modules.log`, `tests.log`, `skills.log`, and `summary.md` when available. Every
operation captures diagnostics always; full command output is in its own log,
independent of bounded stdout previews.

The Go test subprocess clears enclosing job identity and child-submission
environment variables so CLI tests create their own embedded jobs. It retains
the scoped Git credential socket and tool/cache environment.

The tasks use scoped `GOPRIVATE=github.com/colony-2/*` and worker-supported Git
credentials. They do not rewrite global Git credentials or global Go settings.
Go's toolchain is local and module builds use `-mod=readonly`; module checking
uses the Make target's `go mod tidy -diff`. Validation tasks create their own
scratch directories outside the candidate for temporary files and Go/npm caches.
The shared wrapper additionally rejects candidate mutation. Tools must already
be provisioned for each task; the environment task does not install tools.

Worker packages need the exact Go patch used by CI and Node major 24. Private
module access, npm connectivity, and historical revisions used by upgrade tests
are integration prerequisites. Optional metadata collection failures alone do
not fail the gate. Existing Docker and other optional-prerequisite test skips
remain supported and are recorded in the report. A local pass describes this
host's coverage; it does not claim GitHub's Linux AMD64, Linux ARM64 and macOS
matrix passed. GitHub workflows and release behavior remain unchanged.

Native scenarios live in [testdata/recipes/verify-hook](testdata/recipes/verify-hook).
Run them with:

```sh
c2j test validate --file testdata/recipes/verify-hook/verify.test.json
c2j test run --file testdata/recipes/verify-hook/verify.test.json --out-dir /tmp/c2j-verify-tests
```

The suite checks task commands and limits, independent failures, environment
routing, missing results/evidence, launch errors, timeouts, report failure and
native artifact binding. Typed cancellation is tested by
`TestVerifyHookCancellation` in the compiler tests because YAML mock errors carry
messages rather than typed cancellation. Disposable real-hook runs additionally
need the full toolchain, credentials and repository history. Keep an acceptance
report that distinguishes mocked checks from real execution and pending coverage.

The shared build wrapper produces `verification.md` and routes required
verification failure back to implementation. Consuming builds enable its existing
`require_verification: true` input after adoption; this is not a configure-hook
setting. During hook development, use `source_hooks: from_current` to load the
unpublished candidate. Preserve the caller's source policy; normal builds may
load hooks from their upstream execution base. Publication, adoption, wrapper
mutation tests and later base-sourced invocations belong to the parent workflow.
This source change does not modify shared recipe defaults.

Future platform coverage and reproducibility improvements can be evaluated
separately. The skill command intentionally retains CI's current npm resolution
behavior; pinning it requires a coordinated CI change. No remote Actions dispatch,
release operations, child validation jobs, extra lint gate or scheduler is added
by this hook.
