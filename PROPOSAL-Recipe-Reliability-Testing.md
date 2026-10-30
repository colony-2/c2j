**Proposal: run c2j's CI checks through a local build hook**

Add a repository-local verification hook that runs the checks from
[`.github/workflows/test.yaml`](.github/workflows/test.yaml) against the candidate
being developed. This gives the recipe feedback from our existing CI tests before
it presents the work for acceptance. Start with native Linux ARM64 execution on
the current workers, then extend platform coverage as executors become available.

The initial implementation is one `verify` hook containing the three existing
CI checks. Each consuming repository can use the same pattern to bring its own
CI commands into its build recipe.

**Mirror the current test job.** The workflow already defines the commands and
tool versions to use:

| GitHub Actions step | Local hook equivalent |
| --- | --- |
| Check out the repository | Use the recipe's current candidate worktree. |
| Set up Go 1.26.1 | Make Go 1.26.1 available to the check command. |
| Set up Node 24 | Make Node 24 and npm/npx available to the check command. |
| Configure private module access | Set `GOPRIVATE=github.com/colony-2/*` for the invocation and use worker-supported Git credentials. |
| Check module tidiness | `make check-mod` |
| Run tests | `go test -v -tags=integration ./...` |
| Check bundled skill discovery | `npx -y skills add . --list --full-depth` |
| Summarize matrix results | Summarize the three local check results and identify the platform tested. |

The existing `make test` target is insufficient for this purpose: it omits the
`integration` build tag and the skill-discovery command. The local hook should
run all three CI checks. For repeated local verification, add `-count=1` to the
Go invocation so it executes tests again instead of accepting cached results.

**Keep the implementation small.** Add two files:

| Proposed file | Responsibility |
| --- | --- |
| `.c2/recipes/hooks/build/verify.yaml` | Invoke the checks in the candidate worktree, export logs, and return the existing build-hook result. |
| `scripts/ci-checks.sh` | Run the three commands, record each exit status, and write a concise summary plus separate logs to a supplied output directory. |

The script is also the direct developer entrypoint:

```sh
bash scripts/ci-checks.sh /tmp/c2j-ci-results
```

It runs from the repository root, even when the requested change targets a
subdirectory. Run the checks sequentially to fit the current workers. Once tool
and credential setup succeeds, run all three checks even if an earlier one fails.
Capture each exit status and separate log, then produce a combined report showing
every result. Return a nonzero exit status after all checks finish if any failed.

The hook passes its outbox as the script's output directory. It deliberately
captures the script's exit status so it can attach failure logs and return
`failed`; it must not turn a failed command into a passing result. Missing tools,
authentication failures, and timeouts also prevent success. Add an explicit
overall timeout, initially 30 minutes and adjustable after measuring ARM64 runs.

Use the existing hook contract:

- `api_version`: `c2.build-hook/v1`.
- `status`: `passed` only when all three commands succeed; otherwise `failed`
  or an execution error if the hook cannot complete.
- `summary`: which checks passed or failed, and the platform tested.
- `artifact_refs`: the summary, `modules.log`, `tests.log`, and `skills.log`.

The shared recipe already produces `verification.md` and routes verification
failures back to implementation. Enable its existing `require_verification: true`
input for c2j builds so an absent or skipped hook cannot allow completion. This
is a build input, not a new field in the configure hook's settings.

**Use the current worker environment.** Declare the tools through existing
package support, extending the current Go package setup with Node 24 and any
missing shell/Make/Git prerequisites. Check the actual Go patch version against
CI's 1.26.1. Tool declarations apply to the operation running the checks; an
earlier hook installing a tool does not guarantee that a later operation sees it.

Private dependencies need the same read access as CI. Reuse the worker's existing
credential mechanism where available. If a hook must resolve a credential itself,
configure that hook's explicit secret permission using the existing hook policy.
Keep authentication scoped to execution rather than copying CI's global Git/Go
configuration writes into a persistent worker. Record tool versions and the tested
candidate in the summary, and keep logs and generated output outside the managed
candidate files.

This runs the same checks on Linux ARM64, with the locally provisioned tools. It
does not reproduce GitHub's Ubuntu image exactly. Keep Linux AMD64 and macOS runs
in the existing GitHub Actions matrix. There is no need to request other execution
allocations or introduce a scheduler for this first version.

Some existing Go tests skip when Docker or another optional prerequisite is
unavailable. Preserve their current behavior and make those skips visible in the
local report. A successful local run should say that its checks passed on Linux
ARM64 and identify any skipped integration tests; it should not imply that the
entire GitHub matrix ran. Provisioning Docker later can increase coverage without
changing the hook interface.

**Deliver and validate the hook.**

1. Add the script, verify hook, and tool declarations. Leave the existing GitHub
   Actions workflow in place.
2. Run the script on the current ARM64 worker, then invoke it through the shared
   build's verification wrapper. Check all three results and attached logs.
3. Exercise a test failure, untidy modules, failed skill discovery, and a missing
   prerequisite. Each must block verification and expose the relevant diagnostic.
   Confirm an earlier check's failure does not prevent the remaining checks from
   running and that all three results appear in the combined report.
   Confirm the checks leave the candidate unchanged.
4. Enable required verification for c2j builds after the hook is adopted. During
   development, use a validation recipe with `source_hooks: from_current` to
   exercise the new candidate hook; normal builds load hook definitions from their
   upstream execution base by default. See [hook source behavior](GUIDE-Local-Recipe-Hooks.md).

Later, GitHub Actions can call the same script on each matrix runner, keeping the
commands in one place. More comprehensive Actions integration can then add remote
dispatch or additional platforms. The immediate deliverable is local feedback
from the CI checks we already depend on.

Status: proposal only. Based on the current c2j test workflow; no hook or script
has been implemented and no CI checks were run while preparing this document.
